package tunnel

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/db/sqlite"
)

// sqliteOverSSH opens a file through the in-memory SSH server, whose sessions
// run the local sqlite3.
func sqliteOverSSH(t *testing.T, readOnly bool) (db.Conn, *testServer, string) {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		if os.Getenv("DATAVIM_REQUIRE_SQLITE3") == "1" {
			t.Fatal(err)
		}
		t.Skip("no sqlite3 on PATH")
	}
	path := filepath.Join(t.TempDir(), "a b.db")
	if out, err := exec.Command("sqlite3", path, "CREATE TABLE t (id INTEGER PRIMARY KEY, a TEXT, b BLOB)").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	s, h, _ := direct(t)
	ctx := context.Background()
	fn, err := h.pool("").Exec(ctx, "bastion")
	if err != nil {
		t.Fatal(err)
	}
	c, err := sqlite.Driver{}.Open(ctx, db.ConnConfig{Database: path, ReadOnly: readOnly, Exec: fn, ExecHost: "bastion"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, s, path
}

func TestSQLiteOverSSH(t *testing.T) {
	ctx := context.Background()
	c, _, _ := sqliteOverSSH(t, false)
	if _, err := c.Exec(ctx, "INSERT INTO t VALUES (1, 'it''s', x'00ff')"); err != nil {
		t.Fatal(err)
	}
	res, err := c.Exec(ctx, "UPDATE t SET a = 'new' WHERE id = 1")
	if err != nil || res.RowsAffected != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	rs, err := c.Query(ctx, "SELECT id, a, b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	if rs.Rows[0][1].Text != "new" || rs.Rows[0][2].Text != "x'00FF'" || !rs.Rows[0][2].Blob {
		t.Fatalf("%+v", rs.Rows)
	}
	if rs.Columns[1].DatabaseType != "TEXT" {
		t.Fatalf("%+v", rs.Columns)
	}
	tables, err := c.Meta().Tables(ctx, db.Namespace{})
	if err != nil || len(tables) != 1 {
		t.Fatalf("%+v %v", tables, err)
	}
	if _, err := c.Exec(ctx, "DELETE FROM t WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteOverSSHCancel(t *testing.T) {
	ctx := context.Background()
	c, s, _ := sqliteOverSSH(t, false)
	se, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = se.Close() }()
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	_, err = se.Query(cctx, "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<100000000000) SELECT count(*) FROM c")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("cancel took %v", took)
	}
	sawKill := false
	s.mu.Lock()
	for _, cmd := range s.commands {
		sawKill = sawKill || strings.HasPrefix(cmd, "kill -INT ")
	}
	s.mu.Unlock()
	if !sawKill {
		t.Fatal("the running statement was not interrupted through a second channel")
	}
	if err := se.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteOverSSHConnectionLost(t *testing.T) {
	ctx := context.Background()
	c, s, path := sqliteOverSSH(t, false)
	se, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = se.Close() }()
	if _, err := se.Exec(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err := se.Exec(ctx, "INSERT INTO t (a) VALUES ('lost')"); err != nil {
		t.Fatal(err)
	}
	s.dropClients()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err = se.Query(cctx, "SELECT 1")
	if !errors.Is(err, db.ErrConnLost) {
		t.Fatalf("err = %v", err)
	}
	if _, err := se.Query(ctx, "SELECT 1"); !errors.Is(err, db.ErrConnLost) {
		t.Fatalf("reconnected inside a transaction: %v", err)
	}
	// sqlite3 reads end-of-file and rolls back
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := exec.Command("sqlite3", path, "SELECT count(*) FROM t").CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) == "0" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%v: %s", err, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestSQLiteOverSSHReadOnly(t *testing.T) {
	c, _, _ := sqliteOverSSH(t, true)
	if _, err := c.Exec(context.Background(), "INSERT INTO t (a) VALUES ('x')"); err == nil {
		t.Fatal("wrote on a read-only connection")
	}
}
