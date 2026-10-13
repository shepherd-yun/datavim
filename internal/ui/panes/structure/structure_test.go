package structure

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func data() Data {
	def := "'hi'"
	return Data{
		Columns: []db.Column{
			{Name: "id", Type: "bigint", PrimaryKey: true},
			{Name: "note", Type: "text", Nullable: true, Default: &def, Comment: "备注"},
		},
		Indexes: []db.Index{{Name: "users_pkey", Columns: []string{"id"}, Unique: true, Primary: true}},
		ForeignKeys: []db.ForeignKey{{
			Name: "fk", Columns: []string{"user_id"},
			RefTable:   db.TableRef{Namespace: db.Namespace{Schema: "app"}, Name: "users"},
			RefColumns: []string{"id"},
		}},
		DDL: "CREATE TABLE users (\n  id bigint\n);",
	}
}

func TestSections(t *testing.T) {
	m := New()
	m.SetSize(80, 20)
	m.SetData(data())
	v := ansi.Strip(m.View())
	for _, want := range []string{"[Columns]", "PK", "bigint", "NOT NULL", "'hi'", "备注"} {
		if !strings.Contains(v, want) {
			t.Errorf("columns view missing %q:\n%s", want, v)
		}
	}
	m.NextSection()
	if v := ansi.Strip(m.View()); !strings.Contains(v, "PRIMARY") || !strings.Contains(v, "users_pkey") {
		t.Errorf("indexes:\n%s", v)
	}
	m.NextSection()
	if v := ansi.Strip(m.View()); !strings.Contains(v, "app.users(id)") {
		t.Errorf("foreign keys:\n%s", v)
	}
	m.NextSection()
	if v := ansi.Strip(m.View()); !strings.Contains(v, "CREATE TABLE users (") {
		t.Errorf("ddl:\n%s", v)
	}
	m.NextSection()
	if m.Section() != Columns {
		t.Error("NextSection wraps around")
	}
	m.PrevSection()
	if m.Section() != DDL {
		t.Error("PrevSection wraps around")
	}
}

func TestScrollClamps(t *testing.T) {
	m := New()
	m.SetSize(40, 4) // header + blank + 2 body lines
	d := data()
	d.DDL = "l1\nl2\nl3\nl4\nl5"
	m.SetData(d)
	m.PrevSection() // DDL
	m.Bottom()
	if v := ansi.Strip(m.View()); !strings.Contains(v, "l5") || strings.Contains(v, "l3") {
		t.Fatalf("bottom:\n%s", v)
	}
	m.Scroll(-100)
	if v := ansi.Strip(m.View()); !strings.Contains(v, "l1") {
		t.Fatalf("top:\n%s", v)
	}
	m.SetSize(0, 0)
	_ = m.View() // must not panic
}
