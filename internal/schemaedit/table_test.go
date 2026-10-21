package schemaedit

import (
	"errors"
	"slices"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

func users() db.TableSchema {
	zero := "0"
	return db.TableSchema{
		Ref: db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"},
		Columns: []db.Column{
			{Name: "id", Type: "bigint", PrimaryKey: true, AutoIncrement: true, Position: 1},
			{Name: "name", Type: "varchar(100)", Nullable: true, Position: 2},
			{Name: "qty", Type: "int", Default: &zero, Position: 3},
			{Name: "total", Type: "int", Nullable: true, Generated: "qty * 2", Position: 4},
		},
		Indexes: []db.Index{
			{Name: "users_pkey", Columns: []string{"id"}, Unique: true, Primary: true, Constraint: true},
			{Name: "users_name", Columns: []string{"name"}},
			{Name: "users_qty_key", Columns: []string{"qty"}, Unique: true},
		},
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSetColumnMarksTheField(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(1, ColName, " full_name "))
	r := e.Columns()[1]
	if r.Col.Name != "full_name" || r.State() != Changed || !r.Changed(ColName) || r.Changed(ColType) {
		t.Fatalf("row = %+v", r)
	}
	if e.Changes() != 1 {
		t.Fatalf("Changes = %d", e.Changes())
	}
	if e.Before().Columns[1].Name != "name" {
		t.Fatal("the table as read is left alone")
	}
	must(t, e.SetColumn(1, ColName, "name"))
	if e.Columns()[1].State() != Unchanged || e.Changes() != 0 {
		t.Fatal("setting the old name back is no change")
	}
}

func TestTypeInAnotherCaseIsNoChange(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(1, ColType, "VARCHAR(100)"))
	if r := e.Columns()[1]; r.Col.Type != "varchar(100)" || r.State() != Unchanged {
		t.Fatalf("row = %+v", r)
	}
}

func TestEmptyFieldsAreRefused(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	if e.SetColumn(1, ColName, "  ") == nil || e.SetColumn(1, ColType, "") == nil {
		t.Fatal("a column needs a name and a type")
	}
	if e.SetIndexName(1, "") == nil || e.SetIndexColumns(1, " , ") == nil {
		t.Fatal("an index needs a name and columns")
	}
	if e.Changes() != 0 {
		t.Fatal("refused edits change nothing")
	}
}

func TestDefaultEmptyMeansNone(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(2, ColDefault, ""))
	if r := e.Columns()[2]; r.Col.Default != nil || !r.Changed(ColDefault) || r.Text(ColDefault) != "" {
		t.Fatalf("row = %+v", r)
	}
	must(t, e.SetColumn(1, ColDefault, "''"))
	if d := e.Columns()[1].Col.Default; d == nil || *d != "''" {
		t.Fatalf("default = %v", d)
	}
}

func TestGeneratedAndAutoIncrementColumnsRefuse(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	if err := e.SetColumn(3, ColType, "bigint"); err == nil {
		t.Fatal("a generated column's type is left to a console")
	}
	if err := e.SetColumn(3, ColDefault, "1"); err == nil {
		t.Fatal("a generated column has no default")
	}
	if err := e.SetColumn(0, ColDefault, "1"); err == nil {
		t.Fatal("an auto-increment column has no default")
	}
	must(t, e.SetColumn(3, ColComment, "twice qty")) // comments are fine
}

func TestToggleNullable(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.ToggleNullable(1))
	if r := e.Columns()[1]; r.Col.Nullable || r.Text(ColNullable) != "NOT NULL" || !r.Changed(ColNullable) {
		t.Fatalf("row = %+v", r)
	}
}

func TestAddAndDeleteColumns(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	at := e.AddColumn(1) // id, name, (new), qty, total
	if at != 2 || e.Columns()[2].State() != Added || !e.Columns()[2].Col.Nullable {
		t.Fatalf("added at %d: %+v", at, e.Columns()[2])
	}
	if first := e.AddColumn(-1); first != 0 || e.Columns()[0].State() != Added {
		t.Fatal("after -1 puts the column first")
	}
	must(t, e.DeleteColumn(0)) // a new column just goes
	if len(e.Columns()) != 5 || e.Columns()[2].State() != Added {
		t.Fatalf("columns = %+v", e.Columns())
	}
	must(t, e.DeleteColumn(1)) // name, read from the server, is marked
	if r := e.Columns()[1]; r.State() != Deleted {
		t.Fatalf("row = %+v", r)
	}
	if err := e.SetColumn(1, ColName, "x"); !errors.Is(err, ErrDeleted) {
		t.Fatalf("editing a deleted column: %v", err)
	}
	if err := e.DeleteColumn(1); err == nil {
		t.Fatal("deleting twice says so")
	}
}

func TestATableKeepsOneColumn(t *testing.T) {
	s := users()
	s.Columns, s.Indexes = s.Columns[:1], nil
	e := New(s, sqlx.MySQL)
	if err := e.DeleteColumn(0); err == nil {
		t.Fatal("the last column cannot go")
	}
}

func TestUndoRestoresAndReportsPosition(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(1, ColName, "full_name"))
	at := e.AddIndex(2)
	must(t, e.SetIndexColumns(at, "qty"))
	pos, ok := e.Undo()
	if !ok || pos != (Pos{Indexes: true, Row: at}) || len(e.Indexes()[at].Index.Columns) != 0 {
		t.Fatalf("undo = %+v %v, index %+v", pos, ok, e.Indexes()[at])
	}
	if pos, _ = e.Undo(); pos != (Pos{Indexes: true, Row: 2}) || len(e.Indexes()) != 3 { // the row the new index went below
		t.Fatalf("undo = %+v, %d indexes", pos, len(e.Indexes()))
	}
	if pos, _ = e.Undo(); pos != (Pos{Row: 1}) || e.Columns()[1].Col.Name != "name" {
		t.Fatalf("undo = %+v", pos)
	}
	if _, ok := e.Undo(); ok {
		t.Fatal("nothing left to undo")
	}
	must(t, e.ToggleNullable(1))
	e.Reset()
	if e.Changes() != 0 {
		t.Fatal("Reset drops every change")
	}
	if _, ok := e.Undo(); ok {
		t.Fatal("Reset empties the undo history")
	}
}

func TestRenamedColumnFollowsIntoIndexes(t *testing.T) {
	s := users()
	s.Columns[1].Name = "Odd Col"
	s.Indexes[1].Columns = []string{`"Odd Col"`}
	e := New(s, sqlx.Postgres)
	must(t, e.SetColumn(1, ColName, "full name"))
	r := e.Indexes()[1]
	if !slices.Equal(r.Index.Columns, []string{`"full name"`}) || r.State() != Unchanged {
		t.Fatalf("index = %+v, state %v", r.Index, r.State())
	}
	must(t, e.SetIndexColumns(1, "full name, qty"))
	if r := e.Indexes()[1]; !slices.Equal(r.Index.Columns, []string{`"full name"`, "qty"}) || !r.Changed(IdxColumns) {
		t.Fatalf("index = %+v", r.Index)
	}
	must(t, e.SetColumn(1, ColName, "fn"))
	if r := e.Indexes()[1]; !slices.Equal(r.Index.Columns, []string{`"full name"`, "qty"}) {
		t.Fatalf("columns typed for an index stay as typed: %+v", r.Index)
	}
}

func TestNewIndexNamesFollowColumnsAndKind(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	at := e.AddIndex(2)
	must(t, e.SetIndexColumns(at, "name, qty"))
	if got := e.Indexes()[at].Index.Name; got != "users_name_qty_idx" {
		t.Fatalf("name = %q", got)
	}
	must(t, e.CycleIndexKind(at)) // UNIQUE
	if r := e.Indexes()[at]; r.Index.Name != "users_name_qty_key" || !r.Index.Unique || r.Text(IdxKind) != "UNIQUE" {
		t.Fatalf("index = %+v", r.Index)
	}
	must(t, e.CycleIndexKind(at)) // PRIMARY is taken by users_pkey: back to INDEX
	if r := e.Indexes()[at]; r.Index.Unique || r.Index.Primary || r.Text(IdxKind) != "INDEX" {
		t.Fatalf("index = %+v", r.Index)
	}
	must(t, e.SetIndexName(at, "by_name"))
	must(t, e.SetIndexColumns(at, "qty"))
	if got := e.Indexes()[at].Index.Name; got != "by_name" {
		t.Fatalf("a typed name stays: %q", got)
	}
	must(t, e.DeleteIndex(0)) // users_pkey
	must(t, e.CycleIndexKind(at))
	must(t, e.CycleIndexKind(at))
	if r := e.Indexes()[at]; !r.Index.Primary || r.Text(IdxKind) != "PRIMARY" {
		t.Fatalf("with the primary key deleted, PRIMARY is free: %+v", r.Index)
	}
}

func TestSplitList(t *testing.T) {
	for in, want := range map[string][]string{
		"a, b":                     {"a", "b"},
		" a ,, b ":                 {"a", "b"},
		"lower(a), coalesce(b, c)": {"lower(a)", "coalesce(b, c)"},
		`"x,y", z`:                 {`"x,y"`, "z"},
		"`a,b`, 'c,d'":             {"`a,b`", "'c,d'"},
		"":                         nil,
	} {
		if got := splitList(in); !slices.Equal(got, want) {
			t.Errorf("splitList(%q) = %q, want %q", in, got, want)
		}
	}
}
