package structure

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/schemaedit"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/highlight"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
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
	m := New(sqlx.MySQL)
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
	m := New(sqlx.MySQL)
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

func TestDDLIsHighlightedAndSanitized(t *testing.T) {
	m := New(sqlx.MySQL)
	m.SetSize(60, 10)
	d := data()
	d.DDL = "CREATE TABLE users (\n  id bigint -- \x1b[2J\n);"
	m.SetData(d)
	for m.Section() != DDL {
		m.NextSection()
	}
	v := m.View()
	if !strings.Contains(v, theme.Syntax(highlight.Keyword).Render("CREATE")) {
		t.Fatalf("DDL keywords are colored:\n%q", v)
	}
	if strings.Contains(v, "\x1b[2J") {
		t.Fatal("escape sequences from the server are not passed through")
	}
	if !strings.Contains(ansi.Strip(v), "CREATE TABLE users (") {
		t.Fatalf("DDL text:\n%s", ansi.Strip(v))
	}
}

func editable() db.TableSchema {
	d := data()
	return db.TableSchema{
		Ref:     db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"},
		Columns: d.Columns,
		Indexes: d.Indexes,
	}
}

func TestCursorMovesOverRows(t *testing.T) {
	m := New(sqlx.MySQL)
	m.SetSize(80, 20)
	m.SetData(data())
	m.Scroll(1)
	if row, _ := m.Cursor(); row != 1 {
		t.Fatalf("row = %d", row)
	}
	m.Scroll(5)
	if row, _ := m.Cursor(); row != 1 {
		t.Fatal("the cursor stops at the last row")
	}
	m.MoveField(1)
	if _, f := m.Cursor(); f != 0 {
		t.Fatal("fields move only while editing")
	}
	m.NextSection()
	if row, f := m.Cursor(); row != 0 || f != 0 || m.Section() != Indexes {
		t.Fatalf("a new section starts at the top: %d %d", row, f)
	}
}

func TestEditModeMarksFields(t *testing.T) {
	m := New(sqlx.MySQL)
	m.SetSize(80, 20)
	m.SetData(data())
	e := schemaedit.New(editable(), sqlx.MySQL)
	if err := e.SetColumn(1, schemaedit.ColName, "memo"); err != nil {
		t.Fatal(err)
	}
	at := e.AddColumn(1)
	if err := e.SetColumn(at, schemaedit.ColName, "neww"); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteColumn(0); err != nil {
		t.Fatal(err)
	}
	m.SetEdit(e)
	if !m.Editing() {
		t.Fatal("editing")
	}
	m.SetCursor(Columns, 2)
	m.MoveField(1) // the new column's type: the cursor is not on the cells checked below
	v := m.View()
	for name, style := range map[string]lipgloss.Style{
		"memo": theme.Changed, "neww": theme.Inserted, "id  ": theme.Deleted,
	} {
		if !strings.Contains(v, style.Render(name)) {
			t.Errorf("%q is not marked:\n%q", name, v)
		}
	}
	if _, f := m.Cursor(); f != 1 {
		t.Fatalf("field = %d", f)
	}
	m.MoveField(10)
	if _, f := m.Cursor(); f != int(schemaedit.ColumnFields)-1 {
		t.Fatalf("the field cursor stops at the last field: %d", f)
	}
	m.SetEdit(nil)
	if m.Editing() || strings.Contains(m.View(), "neww") {
		t.Fatal("SetEdit(nil) shows the table as read")
	}
}

func TestCursorKeepsItsRowVisible(t *testing.T) {
	m := New(sqlx.MySQL)
	m.SetSize(40, 5) // header line, blank, 3 body lines: table header, rule, one row
	d := data()
	for i := range 10 {
		d.Columns = append(d.Columns, db.Column{Name: fmt.Sprintf("c%d", i), Type: "int"})
	}
	m.SetData(d)
	m.Scroll(6)
	if v := ansi.Strip(m.View()); !strings.Contains(v, "c4") {
		t.Fatalf("row 6 (c4) is visible:\n%s", v)
	}
	m.Top()
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Name") {
		t.Fatalf("the top shows the header:\n%s", v)
	}
}
