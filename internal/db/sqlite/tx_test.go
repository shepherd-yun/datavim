package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
)

const slowInsert = "INSERT INTO big WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<100000000000) SELECT x FROM c"

// bothEngines runs a test on a session of each engine, on a file with the
// fixture and an empty table big; run returns what each statement needs.
func bothEngines(t *testing.T, test func(t *testing.T, c db.Conn, s db.Session)) {
	t.Helper()
	engines := map[string]func(t *testing.T, path string) db.Conn{
		"local": func(t *testing.T, path string) db.Conn { return open(t, db.ConnConfig{Database: path}) },
		"cli":   func(t *testing.T, path string) db.Conn { return openCLIConn(t, path, false) },
	}
	for name, openConn := range engines {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			c := openConn(t, makeFile(t))
			if _, err := c.Exec(ctx, "CREATE TABLE big (x)"); err != nil {
				t.Fatal(err)
			}
			s, err := c.Session(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			test(t, c, s)
		})
	}
}

func execAllOn(t *testing.T, s db.Session, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := s.Exec(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func note(t *testing.T, c db.Conn) string {
	t.Helper()
	rs, err := c.Query(context.Background(), "SELECT note FROM users WHERE id = 1")
	if err != nil {
		t.Fatal(err)
	}
	return rs.Rows[0][0].Text
}

// After RELEASE commits, no transaction is open, so cancelling a statement
// must not report a lost transaction and end the session.
func TestReleaseOfTheOpeningSavepointEndsTheTransaction(t *testing.T) {
	bothEngines(t, func(t *testing.T, c db.Conn, s db.Session) {
		execAllOn(t, s, "SAVEPOINT a", "UPDATE users SET note = 'kept' WHERE id = 1", "RELEASE a")
		if got := note(t, c); got != "kept" {
			t.Fatalf("note = %q: RELEASE did not commit", got)
		}
		_, err := s.Exec(cancelSoon(context.Background()), slowInsert)
		if errors.Is(err, db.ErrConnLost) {
			t.Fatalf("cancelled after the commit: %v", err)
		}
		if _, err := s.Exec(context.Background(), "SELECT 1"); err != nil {
			t.Fatalf("the session should still work: %v", err)
		}
	})
}

// The opposite must hold too: under BEGIN a RELEASE commits nothing.
func TestReleaseInsideBeginKeepsTheTransaction(t *testing.T) {
	bothEngines(t, func(t *testing.T, _ db.Conn, s db.Session) {
		execAllOn(t, s, "BEGIN", "SAVEPOINT a", "RELEASE a")
		_, err := s.Exec(cancelSoon(context.Background()), slowInsert)
		if !errors.Is(err, db.ErrConnLost) {
			t.Fatalf("err = %v, want ErrConnLost: the transaction was open", err)
		}
	})
}

func TestAutomaticRollbackIsReported(t *testing.T) {
	bothEngines(t, func(t *testing.T, c db.Conn, s db.Session) {
		ctx := context.Background()
		execAllOn(t, s, "BEGIN", "UPDATE users SET note = 'x' WHERE id = 1")
		_, err := s.Exec(ctx, "UPDATE OR ROLLBACK users SET id = 2 WHERE id = 1")
		if !errors.Is(err, db.ErrTxRolledBack) {
			t.Fatalf("err = %v, want ErrTxRolledBack", err)
		}
		if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			t.Fatalf("the statement's own error is gone: %v", err)
		}
		if got := note(t, c); got != "hi" {
			t.Fatalf("note = %q, want the rolled back value", got)
		}
		// the probe left nothing open, and the session goes on outside a transaction
		execAllOn(t, s, "UPDATE users SET note = 'after' WHERE id = 1")
		if got := note(t, c); got != "after" {
			t.Fatalf("note = %q: the update should be autocommitted", got)
		}
	})
}

func TestFailedStatementKeepsTheTransaction(t *testing.T) {
	bothEngines(t, func(t *testing.T, c db.Conn, s db.Session) {
		ctx := context.Background()
		execAllOn(t, s, "BEGIN", "UPDATE users SET note = 'x' WHERE id = 1")
		_, err := s.Exec(ctx, "UPDATE users SET id = 2 WHERE id = 1")
		if err == nil || errors.Is(err, db.ErrTxRolledBack) {
			t.Fatalf("err = %v: a plain failure leaves the transaction open", err)
		}
		if got := note(t, c); got != "hi" {
			t.Fatalf("note = %q: nothing is committed yet", got)
		}
		execAllOn(t, s, "COMMIT")
		if got := note(t, c); got != "x" {
			t.Fatalf("note = %q: the transaction should have survived", got)
		}
	})
}
