package schemaedit

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// norm stands in for DDLWriter.NormalizeType: PostgreSQL spells INTEGER int.
func norm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "integer" {
		return "int"
	}
	return s
}

// kinds is "Kind name" per change: the column's new (else old) name or the index's.
func kinds(cs []db.SchemaChange) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		name := ""
		switch {
		case c.New != nil:
			name = c.New.Name
		case c.Old != nil:
			name = c.Old.Name
		case c.NewIndex != nil:
			name = c.NewIndex.Name
		case c.OldIndex != nil:
			name = c.OldIndex.Name
		}
		out[i] = c.Kind.String() + " " + name
	}
	return out
}

func plan(t *testing.T, e *Table) []db.SchemaChange {
	t.Helper()
	cs, err := e.Plan(norm)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func wantKinds(t *testing.T, cs []db.SchemaChange, want ...string) {
	t.Helper()
	if got := kinds(cs); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("changes = %q, want %q", got, want)
	}
}

func TestPlanNothing(t *testing.T) {
	wantKinds(t, plan(t, New(users(), sqlx.MySQL)))
}

func TestPlanRenameAndRetype(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(1, ColName, "full_name"))
	must(t, e.SetColumn(1, ColType, "VARCHAR(200)"))
	cs := plan(t, e)
	wantKinds(t, cs, "RenameColumn full_name", "AlterColumn full_name")
	if cs[1].New.Type != "varchar(200)" || cs[1].Attrs != db.AttrType {
		t.Fatalf("alter = %+v", cs[1])
	}
}

func TestPlanRenameCarriesIntoIndexes(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(1, ColName, "full_name"))
	wantKinds(t, plan(t, e), "RenameColumn full_name") // users_name follows on its own

	must(t, e.SetIndexColumns(1, "full_name, qty"))
	cs := plan(t, e)
	wantKinds(t, cs, "DropIndex users_name", "RenameColumn full_name", "AddIndex users_name")
	if got := strings.Join(cs[2].NewIndex.Columns, ","); got != "full_name,qty" {
		t.Fatalf("index columns = %q", got)
	}
}

func TestPlanNormalizesTypedTypes(t *testing.T) {
	e := New(users(), sqlx.Postgres)
	must(t, e.SetColumn(2, ColType, "integer"))
	wantKinds(t, plan(t, e)) // integer is int
	at := e.AddColumn(3)
	must(t, e.SetColumn(at, ColName, "n"))
	must(t, e.SetColumn(at, ColType, "INTEGER"))
	cs := plan(t, e)
	wantKinds(t, cs, "AddColumn n")
	if cs[0].New.Type != "int" || cs[0].After != "total" {
		t.Fatalf("add = %+v", cs[0])
	}
}

func TestPlanIndexRenameIsDropAndAdd(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetIndexName(1, "users_by_name"))
	wantKinds(t, plan(t, e), "DropIndex users_name", "AddIndex users_by_name")
	must(t, e.SetIndexName(1, "users_name"))
	wantKinds(t, plan(t, e))
}

func TestPlanRefusesDeletedColumnInIndex(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.DeleteColumn(1))
	_, err := e.Plan(norm)
	if err == nil || !strings.Contains(err.Error(), "users_name uses column name") {
		t.Fatalf("err = %v", err)
	}
	must(t, e.DeleteIndex(1))
	cs := plan(t, e)
	wantKinds(t, cs, "DropIndex users_name", "DropColumn name")
	if cs[1].Risk != db.Destructive {
		t.Fatalf("dropping a column is destructive: %+v", cs[1])
	}
}

func TestPlanAddColumnsAfter(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(1, ColName, "full_name"))
	a := e.AddColumn(1)
	must(t, e.SetColumn(a, ColName, "email"))
	must(t, e.SetColumn(a, ColType, "varchar(80)"))
	b := e.AddColumn(a)
	must(t, e.SetColumn(b, ColName, "phone"))
	must(t, e.SetColumn(b, ColType, "varchar(20)"))
	cs := plan(t, e)
	wantKinds(t, cs, "RenameColumn full_name", "AddColumn email", "AddColumn phone")
	if cs[1].After != "full_name" || cs[2].After != "email" {
		t.Fatalf("after = %q, %q", cs[1].After, cs[2].After)
	}
}

func TestPlanRisks(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.ToggleNullable(1))                    // NULL → NOT NULL
	must(t, e.SetColumn(1, ColType, "varchar(50)")) // narrower
	cs := plan(t, e)
	if len(cs) != 1 || cs[0].Risk != db.Lossy {
		t.Fatalf("changes = %+v", cs)
	}
}

func TestPlanChecks(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(e *Table) error
		want string
	}{
		{"new column without a name", func(e *Table) error { e.AddColumn(3); return nil }, "column 5 needs a name"},
		{"new column without a type", func(e *Table) error {
			return e.SetColumn(e.AddColumn(3), ColName, "x")
		}, "column x needs a type"},
		{"two columns with one name", func(e *Table) error { return e.SetColumn(2, ColName, "name") }, "two columns are called name"},
		{"new index without columns", func(e *Table) error { e.AddIndex(2); return nil }, "index 4 needs columns"},
		{"index on a missing column", func(e *Table) error { return e.SetIndexColumns(1, "nope") }, "index users_name: no column nope"},
		{"two indexes with one name", func(e *Table) error { return e.SetIndexName(2, "users_name") }, "two indexes are called users_name"},
		{"swapping names", func(e *Table) error {
			if err := e.SetColumn(1, ColName, "tmp"); err != nil {
				return err
			}
			return e.SetColumn(2, ColName, "name")
		}, "qty cannot take the name name of another column in the same submit"},
		{"reusing a deleted column's name", func(e *Table) error {
			if err := e.DeleteIndex(1); err != nil {
				return err
			}
			if err := e.DeleteColumn(1); err != nil {
				return err
			}
			return e.SetColumn(2, ColName, "name")
		}, "qty cannot take the name name of another column in the same submit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(users(), sqlx.MySQL)
			must(t, tc.edit(e))
			if _, err := e.Plan(norm); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
