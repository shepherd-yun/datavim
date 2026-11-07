package sqlite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/db/dbtest"
)

// osProcess is a local command standing in for one on an SSH server.
type osProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *os.File
	waited chan error
}

func (p *osProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *osProcess) Stdout() io.Reader     { return p.stdout }
func (p *osProcess) Wait() error           { return <-p.waited }

// Close is what closing an SSH channel does: the command sees end-of-file on
// its stdin and goes on running.
func (p *osProcess) Close() error {
	_ = p.stdin.Close()
	return p.stdout.Close()
}

// osExec implements db.ExecFunc with os/exec and sh.
func osExec(ctx context.Context, command string) (db.Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	_ = w.Close()
	p := &osProcess{cmd: cmd, stdin: stdin, stdout: r, waited: make(chan error, 1)}
	go func() { p.waited <- cmd.Wait() }()
	return p, nil
}

func requireSQLite3(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		if os.Getenv("DATAVIM_REQUIRE_SQLITE3") == "1" {
			t.Fatal("sqlite3 is required: ", err)
		}
		t.Skip("no sqlite3 on PATH")
	}
}

func openCLIConn(t *testing.T, path string, readOnly bool) db.Conn {
	t.Helper()
	requireSQLite3(t)
	c, err := Driver{}.Open(context.Background(), db.ConnConfig{Database: path, ReadOnly: readOnly, Exec: osExec, ExecHost: "testhost"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func cliSessionOf(t *testing.T, s db.Session) *cliSession {
	t.Helper()
	return s.(*session).s.(*cliSession)
}

func serverVersion(t *testing.T) (major, minor int) {
	t.Helper()
	out, err := exec.Command("sqlite3", "-version").Output()
	if err != nil {
		t.Fatal(err)
	}
	major, minor, ok := parseVersion(string(out))
	if !ok {
		t.Fatalf("version %q", out)
	}
	return major, minor
}

func TestCLIContract(t *testing.T) {
	requireSQLite3(t)
	path := makeFile(t)
	c := openCLIConn(t, path, false)
	f := contractFixture(path, func(ctx context.Context, cfg db.ConnConfig) (db.Conn, error) {
		cfg.Exec = osExec
		return Driver{}.Open(ctx, cfg)
	})
	f.CancelLosesState = true
	_, minor := serverVersion(t)
	f.NoDropColumn = minor < 35
	dbtest.RunContract(t, c, f)
}

func TestCLIReadOnly(t *testing.T) {
	ctx := context.Background()
	path := makeFile(t)
	c := openCLIConn(t, path, true)
	dir := filepath.Dir(path)
	written := filepath.Join(dir, "written.txt")
	for _, q := range []string{
		"INSERT INTO users (name) VALUES ('x')",
		fmt.Sprintf("SELECT writefile('%s', 'x')", written),
		fmt.Sprintf("ATTACH '%s' AS y", path),
		fmt.Sprintf("VACUUM INTO '%s'", written),
		".shell touch " + written,
		"SELECT edit('x')",
	} {
		if _, err := c.Exec(ctx, q); err == nil {
			t.Errorf("%q succeeded", q)
		}
	}
	if _, err := os.Stat(written); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file was written: %v", err)
	}
	rs, err := c.Query(ctx, "SELECT count(*) FROM users")
	if err != nil || rs.Rows[0][0].Text != "3" {
		t.Fatalf("%+v %v", rs, err)
	}
	if major, minor := serverVersion(t); major == 3 && minor >= 37 {
		if !c.(*conn).eng.(*cliEngine).safe {
			t.Error("-safe not used")
		}
	}
}

func TestCLIWritableGuards(t *testing.T) {
	ctx := context.Background()
	path := makeFile(t)
	c := openCLIConn(t, path, false)
	dir := filepath.Dir(path)
	marker := filepath.Join(dir, "side-effect")
	s, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, q := range []string{
		".shell touch " + marker,
		"/* c */ .system touch " + marker,
		"select 1\ngo\n.shell touch " + marker,
		"select 1\n/\n.shell touch " + marker,
		"#x\n.shell touch " + marker,
		"select 1;\n.shell touch " + marker,
		fmt.Sprintf(`select "edit"('x', 'touch %s')`, marker),
		"select load_extension('x')",
	} {
		if _, err := s.Exec(ctx, q); err == nil {
			t.Errorf("%q succeeded", q)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%q reached the shell: %v", q, err)
		}
	}
	other := filepath.Join(dir, "other.db")
	if err := os.WriteFile(other, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, fmt.Sprintf("ATTACH '%s' AS o", other)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Query(ctx, "SELECT * FROM o.sqlite_master"); err != nil {
		t.Fatal(err)
	}
}

func TestCLIMissingBinary(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(sh, filepath.Join(bin, "sh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	_, err = Driver{}.Open(context.Background(), db.ConnConfig{Database: "/x.db", Exec: osExec, ExecHost: "box"})
	if err == nil || !strings.Contains(err.Error(), "sqlite3 not found on box") || !strings.Contains(err.Error(), "install it") {
		t.Fatalf("err = %v", err)
	}
}

// stubSQLite3 puts a sqlite3 that runs script first on PATH.
func stubSQLite3(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "sqlite3"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

func TestCLIOldVersion(t *testing.T) {
	stubSQLite3(t, "echo '3.25.0 2018-09-15 04:01:47 abc'")
	_, err := Driver{}.Open(context.Background(), db.ConnConfig{Database: "/x.db", Exec: osExec, ExecHost: "box"})
	if err == nil || !strings.Contains(err.Error(), "3.25") || !strings.Contains(err.Error(), "3.26") {
		t.Fatalf("err = %v", err)
	}
}

func TestCLIUnexpectedVersionOutput(t *testing.T) {
	stubSQLite3(t, "echo 'welcome to my custom database'")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Driver{}.Open(ctx, db.ConnConfig{Database: "/x.db", Exec: osExec, ExecHost: "box"})
	if err == nil || !strings.Contains(err.Error(), "welcome to my custom database") {
		t.Fatalf("err = %v", err)
	}
}

func TestCLIHangingStartIsBounded(t *testing.T) {
	stubSQLite3(t, `case "$*" in *-version*) echo '3.54.0 2026-04-09 x' ;; *) sleep 60 ;; esac`)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := (Driver{}).Open(ctx, db.ConnConfig{Database: "/x.db", Exec: osExec, ExecHost: "box"}); err == nil {
		t.Fatal("opened")
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("took %v", took)
	}
}

func TestCLIMissingFile(t *testing.T) {
	requireSQLite3(t)
	path := filepath.Join(t.TempDir(), "missing.db")
	for _, ro := range []bool{false, true} {
		c, err := Driver{}.Open(context.Background(), db.ConnConfig{Database: path, ReadOnly: ro, Exec: osExec})
		if err == nil {
			_ = c.Close()
			t.Fatalf("readOnly=%v: opened a missing file", ro)
		}
		if !strings.Contains(err.Error(), "unable to open") {
			t.Errorf("err = %v", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the file was created: %v", err)
		}
	}
}

func TestCLITrickyPaths(t *testing.T) {
	requireSQLite3(t)
	ctx := context.Background()
	dir := t.TempDir()
	for _, name := range []string{"a b.db", "a'b.db", "数据.db", "a?b#c%d.db", "$(touch pwned).db"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		c := openCLIConn(t, path, false)
		if _, err := c.Exec(ctx, "CREATE TABLE t (a)"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
			t.Fatalf("%s: wrote elsewhere", name)
		}
	}
	if _, err := os.Stat("pwned"); err == nil {
		t.Fatal("the path ran as a command")
	}
}

func TestCLIHomePath(t *testing.T) {
	requireSQLite3(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "x y.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c := openCLIConn(t, "~/x y.db", false)
	if _, err := c.Exec(context.Background(), "CREATE TABLE t (a)"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(home, "x y.db")); err != nil || fi.Size() == 0 {
		t.Fatal("wrote elsewhere")
	}
}

func TestCLIEarlyStopKeepsTx(t *testing.T) {
	ctx := context.Background()
	c := openCLIConn(t, makeFile(t), false)
	s, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, q := range []string{"CREATE TEMP TABLE tmp (a)", "BEGIN", "UPDATE users SET note = 'in tx' WHERE id = 1"} {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	err = s.Stream(ctx, "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<5000) SELECT x FROM c",
		func([]db.ColumnInfo) {}, func([]db.Value) error {
			if n++; n == 10 {
				return db.ErrStop
			}
			return nil
		})
	if err != nil || n != 10 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	rs, err := s.Query(ctx, "SELECT note FROM users WHERE id = 1")
	if err != nil || rs.Rows[0][0].Text != "in tx" {
		t.Fatalf("the transaction was lost: %+v %v", rs, err)
	}
	if _, err := s.Query(ctx, "SELECT * FROM tmp"); err != nil {
		t.Fatalf("the temporary table was lost: %v", err)
	}
	if _, err := s.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	rs, err = s.Query(ctx, "SELECT note FROM users WHERE id = 1")
	if err != nil || rs.Rows[0][0].Text != "hi" {
		t.Fatalf("ROLLBACK did not undo: %+v %v", rs, err)
	}
}

func cancelSoon(ctx context.Context) context.Context {
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(200*time.Millisecond, cancel)
	return cctx
}

const longSelect = "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<100000000000) SELECT count(*) FROM c"

func TestCLICancelOutsideTx(t *testing.T) {
	ctx := context.Background()
	c := openCLIConn(t, makeFile(t), false)
	s, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Exec(ctx, "CREATE TEMP TABLE tmp (a)"); err != nil {
		t.Fatal(err)
	}
	old := cliSessionOf(t, s).p
	start := time.Now()
	_, err = s.Query(cancelSoon(ctx), longSelect)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("cancel took %v", took)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Query(ctx, "SELECT * FROM tmp"); err == nil {
		t.Fatal("the temporary table survived: the old process is still in use")
	}
	assertGone(t, old.pid)
}

// assertGone waits for a process to disappear.
func assertGone(t *testing.T, pid string) {
	t.Helper()
	n, err := strconv.Atoi(pid)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(n, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d is still running", n)
}

func TestCLICloseWhileRunningStopsIt(t *testing.T) {
	ctx := context.Background()
	c := openCLIConn(t, makeFile(t), false)
	s, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pid := cliSessionOf(t, s).p.pid
	done := make(chan error, 1)
	go func() {
		_, err := s.Query(ctx, longSelect)
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	assertGone(t, pid)
	select {
	case err := <-done:
		if !errors.Is(err, db.ErrConnLost) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the running call did not return")
	}
}

func TestCLICancelInTx(t *testing.T) {
	ctx := context.Background()
	c := openCLIConn(t, makeFile(t), false)
	s, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, q := range []string{"BEGIN", "UPDATE users SET note = 'x' WHERE id = 1"} {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.Query(cancelSoon(ctx), longSelect)
	if !errors.Is(err, db.ErrConnLost) || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Query(ctx, "SELECT 1"); !errors.Is(err, db.ErrConnLost) {
		t.Fatalf("next call: %v", err)
	}
	if err := s.Ping(ctx); !errors.Is(err, db.ErrConnLost) {
		t.Fatalf("ping: %v", err)
	}
	// the server's process is gone, so the file is free
	rs, err := c.Query(ctx, "SELECT note FROM users WHERE id = 1")
	if err != nil || rs.Rows[0][0].Text != "hi" {
		t.Fatalf("%+v %v", rs, err)
	}
	if _, err := c.Exec(ctx, "UPDATE users SET note = 'free' WHERE id = 2"); err != nil {
		t.Fatalf("a lock remains: %v", err)
	}
}

func TestCLIPoolEarlyStopReplacesProcess(t *testing.T) {
	ctx := context.Background()
	c := openCLIConn(t, makeFile(t), false)
	eng := c.(*conn).eng.(*cliEngine)
	var pid string
	eng.onStart = func(p *cliProc) { pid = p.pid }
	eng.closeIdle() // the next query starts a process
	n := 0
	err := c.Stream(ctx, "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<100000000000) SELECT x FROM c",
		func([]db.ColumnInfo) {}, func([]db.Value) error {
			if n++; n == 10 {
				return db.ErrStop
			}
			return nil
		})
	if err != nil || n != 10 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if pid == "" {
		t.Fatal("no process started")
	}
	assertGone(t, pid)
	if _, err := c.Query(ctx, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	// no read lock: a write goes through at once
	start := time.Now()
	if _, err := c.Exec(ctx, "UPDATE users SET note = 'w' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("write waited %v", took)
	}
}

func TestCLIPoolLimit(t *testing.T) {
	ctx := context.Background()
	requireSQLite3(t)
	path := makeFile(t)
	var alive, peak atomic.Int64
	counting := func(ctx context.Context, command string) (db.Process, error) {
		p, err := osExec(ctx, command)
		if err != nil || !strings.Contains(command, "PID=") {
			return p, err
		}
		n := alive.Add(1)
		for {
			m := peak.Load()
			if n <= m || peak.CompareAndSwap(m, n) {
				break
			}
		}
		return &countedProcess{Process: p, alive: &alive}, nil
	}
	c, err := Driver{}.Open(ctx, db.ConnConfig{Database: path, Exec: counting})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Query(ctx, "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<300000) SELECT count(*) FROM c"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if p := peak.Load(); p > 4 || p < 1 {
		t.Fatalf("peak %d processes", p)
	}
	eng := c.(*conn).eng.(*cliEngine)
	now := time.Now()
	eng.reap(now.Add(30 * time.Second))
	if n := alive.Load(); n == 0 {
		t.Fatal("reaped before the idle time")
	}
	eng.reap(now.Add(2 * time.Minute))
	if n := alive.Load(); n != 0 {
		t.Fatalf("%d processes left after the idle time", n)
	}
	if _, err := c.Query(ctx, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
}

type countedProcess struct {
	db.Process
	alive *atomic.Int64
	once  sync.Once
}

func (p *countedProcess) Close() error {
	p.once.Do(func() { p.alive.Add(-1) })
	return p.Process.Close()
}

func TestCLILocked(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the busy timeout")
	}
	ctx := context.Background()
	path := makeFile(t)
	holder := open(t, db.ConnConfig{Database: path})
	hs, err := holder.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hs.Close() }()
	if _, err := hs.Exec(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	c := openCLIConn(t, path, false)
	s, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	start := time.Now()
	_, err = s.Exec(ctx, "UPDATE users SET note = 'x' WHERE id = 1")
	if err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took < 4*time.Second || took > 15*time.Second {
		t.Fatalf("took %v", took)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCLIProcessDies(t *testing.T) {
	ctx := context.Background()
	c := openCLIConn(t, makeFile(t), false)
	for _, inTx := range []bool{false, true} {
		s, err := c.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if inTx {
			if _, err := s.Exec(ctx, "BEGIN"); err != nil {
				t.Fatal(err)
			}
		}
		pid, err := strconv.Atoi(cliSessionOf(t, s).p.pid)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err = s.Query(cctx, "SELECT 1")
		cancel()
		if !errors.Is(err, db.ErrConnLost) || !strings.Contains(err.Error(), "may still finish on the server") {
			t.Fatalf("inTx=%v: err = %v", inTx, err)
		}
		if inTx && !strings.Contains(err.Error(), "uncommitted changes will not be kept") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), "rolled back") {
			t.Fatalf("claims a rollback it cannot know: %v", err)
		}
		_, err = s.Query(ctx, "SELECT 1")
		if inTx && !errors.Is(err, db.ErrConnLost) {
			t.Fatalf("reconnected inside a transaction: %v", err)
		}
		if !inTx && err != nil {
			t.Fatalf("outside a transaction the next call starts afresh: %v", err)
		}
		_ = s.Close()
	}
}

func TestCLINoStatements(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := openCLIConn(t, makeFile(t), false)
	s, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, q := range []string{"", "   \n", "-- only a comment", "/* c */", ";", ";;"} {
		if rs, err := s.Query(ctx, q); err != nil || len(rs.Rows) != 0 {
			t.Errorf("%q: %+v %v", q, rs, err)
		}
		if _, err := s.Exec(ctx, q); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
}

func TestCLIMultiStatement(t *testing.T) {
	ctx := context.Background()
	c := openCLIConn(t, makeFile(t), false)
	rs, err := c.Query(ctx, "INSERT INTO users (name) VALUES ('m'); SELECT count(*) FROM users;")
	if err != nil || rs.Rows[0][0].Text != "4" {
		t.Fatalf("%+v %v", rs, err)
	}
	if _, err := c.Exec(ctx, "UPDATE users SET note = 'c' WHERE id = 1; bad; UPDATE users SET note = 'd' WHERE id = 1"); err == nil {
		t.Fatal("want an error")
	}
	rs, err = c.Query(ctx, "SELECT note FROM users WHERE id = 1")
	if err != nil || rs.Rows[0][0].Text != "c" {
		t.Fatalf("stopped at the error: %+v %v", rs, err)
	}
}

func TestCLILargeValues(t *testing.T) {
	ctx := context.Background()
	c := openCLIConn(t, makeFile(t), false)
	if _, err := c.Exec(ctx, "CREATE TABLE big (id INTEGER PRIMARY KEY, t TEXT, b BLOB)"); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("长", 3<<20) // about 9.4 MB
	values := []string{long, "a\r\nb", "a\xff\xfeb", "x'00ff'", "''", "line\n.print oops\n", "DV_marker"}
	for i, v := range values {
		if _, err := c.Exec(ctx, "INSERT INTO big VALUES (?, ?, ?)", i, v, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	rs, err := c.Query(ctx, "SELECT t, b FROM big ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range values {
		if rs.Rows[i][0].Text != v || rs.Rows[i][0].Blob {
			t.Errorf("value %d text did not survive (%d bytes)", i, len(rs.Rows[i][0].Text))
		}
		if want := blobText([]byte(v)); rs.Rows[i][1].Text != want || !rs.Rows[i][1].Blob {
			t.Errorf("value %d blob did not survive", i)
		}
	}
	rs, err = c.Query(ctx, "SELECT id, t, 1+1 AS e FROM big WHERE id < 0")
	if err != nil {
		t.Fatal(err)
	}
	want := []db.ColumnInfo{{Name: "id", DatabaseType: "INTEGER"}, {Name: "t", DatabaseType: "TEXT"}, {Name: "e", DatabaseType: "ANY"}}
	if diff := cmp.Diff(want, rs.Columns); diff != "" {
		t.Errorf("empty result (-want +got):\n%s", diff)
	}
}

func TestCLIBothEnginesAgree(t *testing.T) {
	ctx := context.Background()
	path := makeFile(t)
	local := open(t, db.ConnConfig{Database: path})
	remote := openCLIConn(t, path, false)
	q := `SELECT id, name, 1.5, 2.0, x'00ff', 'x''00ff''', NULL, 9007199254740993 FROM users ORDER BY id`
	a, err := local.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	b, err := remote.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(a.Rows, b.Rows); diff != "" {
		t.Errorf("rows (-local +cli):\n%s", diff)
	}
	var at, bt []string
	for i := range a.Columns {
		at, bt = append(at, a.Columns[i].DatabaseType), append(bt, b.Columns[i].DatabaseType)
	}
	if diff := cmp.Diff(at, bt); diff != "" {
		t.Errorf("types (-local +cli):\n%s", diff)
	}
}

func TestCleanError(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Parse error near line 10: no such table: nope", "no such table: nope"},
		{"Error: near line 10: no such table: nope", "no such table: nope"},
		{"Runtime error near line 3: database is locked (5)", "database is locked (5)"},
		{"Parse error near line 1: near \"x\": syntax error\n  select x y\n   ^--- error here", `near "x": syntax error`},
		{"Error: unable to open database", "unable to open database"},
		{"something else", "something else"},
	}
	for _, tt := range tests {
		if got := cleanError(tt.in); got != tt.want {
			t.Errorf("cleanError(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
