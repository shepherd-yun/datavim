package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/schemadiff"
	"github.com/shepherd-yun/datavim/internal/schemaedit"
	"github.com/shepherd-yun/datavim/internal/sqlx"
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

// Constraints have no CREATE INDEX text to read their options from.
func TestConstraintIndexOptions(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	for _, q := range []string{
		`CREATE TABLE u1 (a TEXT, b TEXT, UNIQUE (a COLLATE NOCASE DESC, b))`,
		`CREATE TABLE u2 (a TEXT COLLATE NOCASE UNIQUE)`, // the column's own collation
		`CREATE TABLE u3 (a TEXT COLLATE NOCASE, UNIQUE (a COLLATE BINARY))`,
		`CREATE TABLE p1 (a TEXT, b INT, PRIMARY KEY (a COLLATE NOCASE, b DESC)) WITHOUT ROWID`,
		`CREATE TABLE p2 (a TEXT PRIMARY KEY, b INT)`,
	} {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		table string
		want  []string
	}{
		{"u1", []string{"COLLATE NOCASE DESC", ""}},
		{"u2", nil},
		{"u3", []string{"COLLATE BINARY"}},
		{"p1", []string{"COLLATE NOCASE", "DESC"}},
		{"p2", nil},
	}
	for _, tt := range cases {
		idx, err := c.Meta().Indexes(ctx, db.TableRef{Name: tt.table})
		if err != nil {
			t.Fatal(err)
		}
		if len(idx) != 1 {
			t.Fatalf("%s: %+v", tt.table, idx)
		}
		if diff := cmp.Diff(tt.want, idx[0].Suffixes); diff != "" {
			t.Errorf("%s (-want +got):\n%s", tt.table, diff)
		}
	}
	s, err := c.Meta().TableSchema(ctx, db.TableRef{Name: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	stmts, _ := dialect{}.DDL().CreateTable(s)
	if !strings.Contains(stmts[0], `PRIMARY KEY ("a" COLLATE NOCASE, "b" DESC)`) {
		t.Errorf("primary key lost its options:\n%s", stmts[0])
	}
}

// price(usd) is a column, though it reads like a call.
func TestColumnNamesWithParentheses(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	for _, q := range []string{
		`CREATE TABLE odd ("price(usd)" TEXT NOT NULL, v TEXT, PRIMARY KEY ("price(usd)"))`,
		`CREATE INDEX odd_p ON odd ("price(usd)", lower(v))`,
	} {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	ref := db.TableRef{Name: "odd"}
	s, err := c.Meta().TableSchema(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	s.Ref = db.TableRef{Name: "copy"}
	for i := range s.Indexes {
		s.Indexes[i].Name = strings.Replace(s.Indexes[i].Name, "odd", "copy", 1)
	}
	stmts, _ := dialect{}.DDL().CreateTable(s)
	for _, q := range stmts {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	rs, err := c.Query(ctx, "SELECT sql FROM sqlite_master WHERE name = 'copy_p'")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rs.Rows[0][0].Text, `CREATE INDEX "copy_p" ON "copy" ("price(usd)", lower(v))`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	// Modify Table: a renamed column, and a new index over it
	s, err = c.Meta().TableSchema(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	e := schemaedit.New(s, sqlx.SQLite)
	if err := e.SetColumn(0, schemaedit.ColName, "cost(usd)"); err != nil {
		t.Fatal(err)
	}
	at := e.AddIndex(len(e.Indexes()) - 1)
	if err := e.SetIndexColumns(at, "cost(usd), upper(v)"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetIndexName(at, "odd_new"); err != nil {
		t.Fatal(err)
	}
	changes, err := e.Plan(normalizeType)
	if err != nil {
		t.Fatal(err)
	}
	got, manual := dialect{}.DDL().AlterTable(ref, changes)
	want := []string{
		`ALTER TABLE "odd" RENAME COLUMN "price(usd)" TO "cost(usd)"`,
		`CREATE INDEX "odd_new" ON "odd" ("cost(usd)", upper(v))`,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if len(manual) != 0 {
		t.Fatalf("manual: %+v", manual)
	}
	for _, q := range got {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// recreate writes table under the name copy_<table>, runs it and returns the
// statement that created the table.
func recreate(t *testing.T, c db.Conn, table string) string {
	t.Helper()
	ctx := context.Background()
	s, err := c.Meta().TableSchema(ctx, db.TableRef{Name: table})
	if err != nil {
		t.Fatal(err)
	}
	s.Ref = db.TableRef{Name: "copy_" + table}
	stmts, _ := dialect{}.DDL().CreateTable(s)
	for _, q := range stmts {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return stmts[0]
}

func TestRecreatedTableKeepsItsOptions(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	cases := []struct{ table, def, want string }{
		{"plain", "(a TEXT PRIMARY KEY)", ""},
		{"norowid", "(a TEXT PRIMARY KEY, b INT) WITHOUT ROWID", "WITHOUT ROWID"},
		{"strict", "(a INT, b ANY) STRICT", "STRICT"},
		{"both", "(a INT PRIMARY KEY, b ANY) STRICT, WITHOUT ROWID", "STRICT, WITHOUT ROWID"},
	}
	for _, tt := range cases {
		if _, err := c.Exec(ctx, "CREATE TABLE "+tt.table+" "+tt.def); err != nil {
			t.Fatal(err)
		}
		recreate(t, c, tt.table)
		for _, name := range []string{tt.table, "copy_" + tt.table} {
			s, err := c.Meta().TableSchema(ctx, db.TableRef{Name: name})
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Options[tableOptions]; got != tt.want {
				t.Errorf("%s: options = %q, want %q", name, got, tt.want)
			}
		}
	}
}

// A table's options cannot be changed without a rebuild, so a difference is left
// to the user whichever side has them.
func TestDifferentTableOptionsAreManual(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	for _, q := range []string{"CREATE TABLE a (k TEXT PRIMARY KEY)", "CREATE TABLE b (k TEXT PRIMARY KEY) WITHOUT ROWID"} {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	a, err := c.Meta().TableSchema(ctx, db.TableRef{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Meta().TableSchema(ctx, db.TableRef{Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]db.TableSchema{{a, b}, {b, a}} {
		changes := schemadiff.DiffTable(pair[0], pair[1], nil)
		if len(changes) != 1 || changes[0].Kind != db.AlterTableOption {
			t.Fatalf("changes = %+v", changes)
		}
		stmts, manual := dialect{}.DDL().AlterTable(a.Ref, changes)
		if len(stmts) != 0 || len(manual) != 1 {
			t.Fatalf("stmts %v, manual %+v", stmts, manual)
		}
	}
}

// Only the column-level spelling keeps INTEGER PRIMARY KEY DESC from becoming a
// rowid alias, which would change what NULL does and how rows are numbered.
func TestRecreatedDescendingKeyStaysNoAlias(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	cases := []struct {
		table, def string
		alias      bool
		want       string
	}{
		{"column_desc", "(id INTEGER PRIMARY KEY DESC, v TEXT)", false, `"id" integer NOT NULL PRIMARY KEY DESC`},
		{"named_desc", "(id INTEGER CONSTRAINT k PRIMARY KEY DESC, v TEXT)", false, `"id" integer NOT NULL CONSTRAINT "k" PRIMARY KEY DESC`},
		{"table_desc", "(id INTEGER, v TEXT, PRIMARY KEY (id DESC))", true, `PRIMARY KEY ("id")`},
		{"int_desc", "(id INT PRIMARY KEY DESC, v TEXT)", false, `PRIMARY KEY ("id" DESC)`},
		{"norowid_desc", "(id INTEGER PRIMARY KEY DESC, v TEXT) WITHOUT ROWID", false, `PRIMARY KEY ("id" DESC)`},
	}
	for _, tt := range cases {
		if _, err := c.Exec(ctx, "CREATE TABLE "+tt.table+" "+tt.def); err != nil {
			t.Fatal(err)
		}
		if ddl := recreate(t, c, tt.table); !strings.Contains(ddl, tt.want) {
			t.Errorf("%s: no %s in\n%s", tt.table, tt.want, ddl)
		}
		cols, err := c.Meta().Columns(ctx, db.TableRef{Name: "copy_" + tt.table})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(tt.def, "INTEGER") && cols[0].AutoIncrement != tt.alias {
			t.Errorf("%s: alias = %v, want %v", tt.table, cols[0].AutoIncrement, tt.alias)
		}
	}
}
