package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
)

// TestCreateTableRecreatesATable writes the table the engine describes and
// runs the result, which is what a schema sync does.
func TestCreateTableRecreatesATable(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	for _, q := range []string{
		`CREATE TABLE src (
			id INTEGER PRIMARY KEY,
			expr INT DEFAULT (1+1),
			fn TEXT DEFAULT (upper('x')),
			lit INT DEFAULT 5,
			neg INT DEFAULT -5,
			str TEXT DEFAULT 'a''b',
			concat TEXT DEFAULT ('a' || 'b'),
			bare TEXT DEFAULT abc,
			nul TEXT DEFAULT NULL,
			bin BLOB DEFAULT x'00ff',
			stamp TEXT DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX i_expr ON src (lit+neg)`,
		`CREATE INDEX i_fn ON src (lower(str) DESC)`,
		`CREATE INDEX i_opts ON src (str COLLATE NOCASE DESC, lit)`,
	} {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	s, err := c.Meta().TableSchema(ctx, db.TableRef{Name: "src"})
	if err != nil {
		t.Fatal(err)
	}
	s.Ref = db.TableRef{Name: "dst"}
	for i := range s.Indexes {
		s.Indexes[i].Name = strings.Replace(s.Indexes[i].Name, "i_", "j_", 1)
	}
	stmts, manual := dialect{}.DDL().CreateTable(s)
	if len(manual) != 0 {
		t.Fatalf("manual: %+v", manual)
	}
	for _, q := range stmts {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	if _, err := c.Exec(ctx, "INSERT INTO dst (id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	rs, err := c.Query(ctx, "SELECT expr, fn, lit, neg, str, concat, bare, nul, bin FROM dst")
	if err != nil {
		t.Fatal(err)
	}
	want := []db.Value{
		{Text: "2"},
		{Text: "X"},
		{Text: "5"},
		{Text: "-5"},
		{Text: "a'b"},
		{Text: "ab"},
		{Text: "abc"},
		{Null: true},
		{Text: "x'00FF'", Blob: true},
	}
	if diff := cmp.Diff(want, rs.Rows[0]); diff != "" {
		t.Errorf("defaults (-want +got):\n%s", diff)
	}

	rs, err = c.Query(ctx, "SELECT sql FROM sqlite_master WHERE type = 'index' AND name LIKE 'j\\_%' ESCAPE '\\' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rs.Rows {
		got = append(got, r[0].Text)
	}
	wantIdx := []string{
		`CREATE INDEX "j_expr" ON "dst" ((lit+neg))`,
		`CREATE INDEX "j_fn" ON "dst" (lower(str) DESC)`,
		`CREATE INDEX "j_opts" ON "dst" ("str" COLLATE NOCASE DESC, "lit")`,
	}
	if diff := cmp.Diff(wantIdx, got); diff != "" {
		t.Errorf("indexes (-want +got):\n%s", diff)
	}
}

// Rows of a table are located by plain column names, so a key's options stay
// out of Columns.
func TestIndexColumnsAreNames(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	for _, q := range []string{
		`CREATE TABLE k (a TEXT NOT NULL, b INT)`,
		`CREATE UNIQUE INDEX k_a ON k (a COLLATE NOCASE DESC)`,
	} {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := c.Meta().Indexes(ctx, db.TableRef{Name: "k"})
	if err != nil {
		t.Fatal(err)
	}
	want := []db.Index{{Name: "k_a", Columns: []string{"a"}, Unique: true, Method: "btree", Suffixes: []string{"COLLATE NOCASE DESC"}}}
	if diff := cmp.Diff(want, idx); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestOnlyARowidAliasCountsAsAutoIncrement(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	cases := []struct {
		name, def string
		want      bool
	}{
		{"alias", "(id INTEGER PRIMARY KEY, v TEXT)", true},
		{"autoinc", "(id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)", true},
		{"lower", "(id integer primary key, v TEXT)", true},
		{"norowid", "(id INTEGER PRIMARY KEY, v TEXT) WITHOUT ROWID", false},
		{"descending", "(id INTEGER PRIMARY KEY DESC, v TEXT)", false},
		{"plainint", "(id INT PRIMARY KEY, v TEXT)", false},
		{"composite", "(id INTEGER, v TEXT, PRIMARY KEY (id, v))", false},
	}
	for _, tt := range cases {
		if _, err := c.Exec(ctx, "CREATE TABLE "+tt.name+" "+tt.def); err != nil {
			t.Fatal(err)
		}
		cols, err := c.Meta().Columns(ctx, db.TableRef{Name: tt.name})
		if err != nil {
			t.Fatal(err)
		}
		if cols[0].AutoIncrement != tt.want {
			t.Errorf("%s: AutoIncrement = %v, want %v", tt.def, cols[0].AutoIncrement, tt.want)
		}
	}
}
