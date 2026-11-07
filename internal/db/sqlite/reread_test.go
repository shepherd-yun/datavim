package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
)

func TestDateColumnOfATableNamedLikeTheRewrite(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	for _, q := range []string{"CREATE TABLE _dv (d DATE)", "INSERT INTO _dv VALUES ('2026-01-02')"} {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	rs, err := c.Query(ctx, "SELECT d FROM _DV")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([][]db.Value{{{Text: "2026-01-02"}}}, rs.Rows); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

// failsOnRead lets the first query through and refuses the one after it.
type failsOnRead struct {
	sqlRunner
	err     error
	queries int
}

func (r *failsOnRead) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	r.queries++
	if r.queries > 1 {
		return nil, r.err
	}
	return r.sqlRunner.QueryContext(ctx, query, args...)
}

// A busy file or a cancelled query can fail the second read of a date column:
// that is an error for the caller, not a crash.
func TestRereadFailureIsReturned(t *testing.T) {
	ctx := context.Background()
	path := makeFile(t)
	c := open(t, db.ConnConfig{Database: path})
	if _, err := c.Exec(ctx, "CREATE TABLE d (a DATE)"); err != nil {
		t.Fatal(err)
	}
	sqldb, err := sql.Open("sqlite", localDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqldb.Close() }()
	boom := errors.New("database is locked")
	r := &failsOnRead{sqlRunner: sqldb, err: boom}
	err = localStream(ctx, r, "SELECT a FROM d", func([]db.ColumnInfo) {}, func([]db.Value) error { return nil })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}
