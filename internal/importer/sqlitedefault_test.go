package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/db/sqlite"
)

// An empty cell must store what the column's own DEFAULT stores, whichever way
// the default is written: SQLite's VALUES has no DEFAULT keyword.
func TestSQLiteDefaultSQLMatchesTheColumnDefault(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "d.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := sqlite.Driver{}.Open(ctx, db.ConnConfig{Database: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	defaults := []string{
		`abc`, `"d q"`, `"a""b"`, `'s''t'`, `5`, `-5`, `1.5`, `x'00ff'`, `NULL`, `null`, `TRUE`, `false`, `(1+1)`, `(upper('x'))`,
	}
	for i, d := range defaults {
		table := "t" + string(rune('a'+i))
		if _, err := c.Exec(ctx, "CREATE TABLE "+table+" (k INT, v DEFAULT "+d+")"); err != nil {
			t.Fatal(err)
		}
		cols, err := c.Meta().Columns(ctx, db.TableRef{Name: table})
		if err != nil {
			t.Fatal(err)
		}
		sql := TargetOf("sqlite", cols[1]).DefaultSQL
		if _, err := c.Exec(ctx, "INSERT INTO "+table+" (k, v) VALUES (1, "+sql+")"); err != nil {
			t.Errorf("DEFAULT %s written as %s: %v", d, sql, err)
			continue
		}
		if _, err := c.Exec(ctx, "INSERT INTO "+table+" (k) VALUES (2)"); err != nil {
			t.Fatal(err)
		}
		rs, err := c.Query(ctx, "SELECT v, typeof(v) FROM "+table+" ORDER BY k")
		if err != nil {
			t.Fatal(err)
		}
		if rs.Rows[0][0] != rs.Rows[1][0] || rs.Rows[0][1] != rs.Rows[1][1] {
			t.Errorf("DEFAULT %s written as %s: stored %+v, the column's default is %+v", d, sql, rs.Rows[0], rs.Rows[1])
		}
	}
}

// sqlite3 builds without double-quoted strings would refuse the plain spelling.
func TestSQLiteDefaultSQLQuotesText(t *testing.T) {
	cases := map[string]string{
		`abc`: `'abc'`, `"d q"`: `'d q'`, `"a""b"`: `'a"b'`, `"it's"`: `'it''s'`,
		`null`: `null`, `CURRENT_TIMESTAMP`: `CURRENT_TIMESTAMP`, `5`: `5`, `upper('x')`: `upper('x')`, `1+1`: `1+1`,
	}
	for in, want := range cases {
		if got := sqliteDefaultSQL(in); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}
