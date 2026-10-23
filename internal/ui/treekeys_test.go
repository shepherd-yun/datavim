package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/ui/panes/structure"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// expandUsers selects local/app/users and expands it with l.
func expandUsers(t *testing.T, m *Model, b *fakeBackend) {
	t.Helper()
	b.conn.setSchemas(usersSchema())
	selectNode(t, m, "local", "app", "users")
	press(t, m, "l")
}

func TestLExpandsATableAndEnterOpensIt(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	if len(m.tabs) != 0 {
		t.Fatal("l on a table expands it, it opens no tab")
	}
	s := screen(m)
	for _, want := range []string{"Columns", "id  bigint PK", "Keys", "Indexes"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	press(t, m, "enter")
	if !strings.Contains(screen(m), "[1]users") {
		t.Fatalf("Enter on a table opens its data:\n%s", screen(m))
	}
}

func TestEnterOnGroupToggles(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Keys")
	press(t, m, "enter")
	if !m.tree.Selected().Expanded {
		t.Fatal("Enter opens a group")
	}
	press(t, m, "enter")
	if m.tree.Selected().Expanded {
		t.Fatal("Enter again closes it")
	}
}

func TestEnterOnDatabaseToggles(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app")
	press(t, m, "enter")
	if m.tree.Selected().Expanded {
		t.Fatal("Enter on an expanded database closes it")
	}
	press(t, m, "enter")
	if !m.tree.Selected().Expanded {
		t.Fatal("Enter again opens it")
	}
}

func TestZKeysFold(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	press(t, m, "z", "M")
	if s := screen(m); strings.Contains(s, "users") {
		t.Fatalf("zM closes everything:\n%s", s)
	}
	press(t, m, "z", "R")
	if s := screen(m); !strings.Contains(s, "id  bigint PK") || !strings.Contains(s, "PRIMARY") {
		t.Fatalf("zR opens everything loaded:\n%s", s)
	}
	if prod := m.tree.Roots()[1]; prod.Expanded || prod.Loading {
		t.Fatal("zR loads nothing")
	}
	selectNode(t, m, "local", "app", "users", "Columns", "id")
	press(t, m, "z", "c")
	if n := m.tree.Selected(); n.Name != "Columns" || n.Expanded {
		t.Fatalf("zc on a column closes Columns; selected %+v", n)
	}
	press(t, m, "z", "a")
	if !m.tree.Selected().Expanded {
		t.Fatal("za opens it again")
	}
}

func TestRefreshFromALeafReloadsTheTable(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Keys")
	press(t, m, "l") // the user opened Keys
	s := usersSchema()
	s.Columns = append(s.Columns, db.Column{Name: "email", Type: "text", Position: 3})
	b.conn.setSchemas(s)
	selectNode(t, m, "local", "app", "users", "Columns", "id")
	press(t, m, "space", "r")
	if !strings.Contains(screen(m), "email  text") {
		t.Fatalf("refresh on a column reloads the table:\n%s", screen(m))
	}
	users := m.tree.FindTable("local", db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"})
	if !users.Children[1].Expanded {
		t.Fatal("Keys stays open across the reload")
	}
	if sel := m.tree.Selected(); sel.Table() != users {
		t.Fatalf("cursor stays in the table, got %s", sel.Name)
	}
}

func TestRefreshOnAnUnloadedTableDoesNotExpandIt(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setSchemas(usersSchema())
	selectNode(t, m, "local", "app", "users")
	press(t, m, "space", "r")
	if n := m.tree.Selected(); n.Expanded || n.Loaded {
		t.Fatalf("users = %+v", n)
	}
}

func structTab(t *testing.T, m *Model) *structureTab {
	t.Helper()
	st, ok := m.activeTab().(*structureTab)
	if !ok {
		t.Fatalf("active tab %T:\n%s", m.activeTab(), screen(m))
	}
	return st
}

func TestEnterOnColumnOpensItsStructureRow(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	press(t, m, "enter")
	st := structTab(t, m)
	if st.view.Section() != structure.Columns || st.view.CursorName() != "name" || st.ed != nil {
		t.Fatalf("section %v cursor %q editing %v", st.view.Section(), st.view.CursorName(), st.ed != nil)
	}
}

func TestEnterOnIndexOpensTheIndexesSection(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Indexes", "PRIMARY")
	press(t, m, "enter")
	st := structTab(t, m)
	if st.view.Section() != structure.Indexes || st.view.CursorName() != "PRIMARY" {
		t.Fatalf("section %v cursor %q", st.view.Section(), st.view.CursorName())
	}
}

func TestEOnColumnModifiesAtIt(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	press(t, m, "e")
	st := structTab(t, m)
	if st.ed == nil || st.view.CursorName() != "name" || st.changes() != 0 {
		t.Fatalf("editing %v cursor %q changes %d", st.ed != nil, st.view.CursorName(), st.changes())
	}
}

func TestDDOnColumnStagesItsDrop(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	press(t, m, "d", "d")
	st := structTab(t, m)
	if st.ed == nil || st.changes() != 1 {
		t.Fatalf("editing %v changes %d", st.ed != nil, st.changes())
	}
	if execs := b.conn.execList(); len(execs) != 0 {
		t.Fatalf("dd only stages; ran %q", execs)
	}
	if !strings.Contains(screen(m), "drop name staged") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestDDOnColumnOfReadOnlyConnection(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	if err := b.SetReadOnly("local", true); err != nil {
		t.Fatal(err)
	}
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	press(t, m, "d", "d")
	if !strings.Contains(screen(m), "read-only") {
		t.Fatalf("status:\n%s", screen(m))
	}
	if err := b.SetReadOnly("local", false); err != nil {
		t.Fatal(err)
	}
	if st, ok := m.activeTab().(*structureTab); ok {
		press(t, m, "e") // modifying later must not apply the refused drop
		if st.changes() != 0 {
			t.Fatal("a refused dd left a pending drop")
		}
	}
}

func TestStaleLeafDropsNothing(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	s := usersSchema()
	s.Columns = s.Columns[:1] // name was dropped elsewhere; the tree is stale
	b.conn.setSchemas(s)
	press(t, m, "d", "d")
	st := structTab(t, m)
	if st.changes() != 0 {
		t.Fatal("a stale leaf staged a drop of another row")
	}
	if !strings.Contains(screen(m), "name is not in users any more") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestForeignKeyLeaf(t *testing.T) {
	m, b := newTestModel(t)
	app := db.Namespace{Database: "app"}
	b.conn.setTables([]db.Table{{Name: "orgs"}, {Name: "users"}})
	s := usersSchema()
	s.ForeignKeys = []db.ForeignKey{{Name: "fk_org", Columns: []string{"name"}, RefTable: db.TableRef{Namespace: app, Name: "orgs"}, RefColumns: []string{"id"}}}
	b.conn.setSchemas(s)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "l")
	selectNode(t, m, "local", "app", "users", "Foreign Keys", "fk_org")
	press(t, m, "e")
	if !strings.Contains(screen(m), "foreign keys cannot be modified yet") {
		t.Fatalf("e on a foreign key:\n%s", screen(m))
	}
	press(t, m, "enter")
	if n := m.tree.Selected(); n.Name != "orgs" || n.Kind != tree.KindTable {
		t.Fatalf("Enter on a foreign key selects the referenced table, got %+v", n)
	}
}

func TestViewColumnsCannotBeModified(t *testing.T) {
	m, _ := newTestModel(t)
	selectNode(t, m, "local", "app", "active")
	press(t, m, "l")
	selectNode(t, m, "local", "app", "active", "Columns", "id")
	press(t, m, "d", "d")
	if !strings.Contains(screen(m), "views cannot be modified") {
		t.Fatalf("dd on a view's column:\n%s", screen(m))
	}
	if len(m.tabs) != 0 {
		t.Fatal("no tab opened")
	}
}

func TestDDOnGroupExplains(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns")
	press(t, m, "d", "d")
	if !strings.Contains(screen(m), "select a column or index to drop it") {
		t.Fatalf("dd on a group:\n%s", screen(m))
	}
}

func TestStructureKeysWorkOnALeaf(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	press(t, m, "space", "S")
	if st := structTab(t, m); st.ref.Name != "users" {
		t.Fatalf("<space>S on a column opens its table, got %s", st.ref.Name)
	}
}

func TestModifyTableReloadsTheTreeTable(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	renameNameColumn(t, m, b, "local")
	s := usersSchema()
	s.Columns[1].Name = "name_x"
	b.conn.setSchemas(s)
	press(t, m, "space", "w", "y")
	users := m.tree.FindTable("local", db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"})
	if got := users.Children[0].Children[1].Name; got != "name_x" {
		t.Fatalf("tree column = %s, want name_x after the submit", got)
	}
}

func TestNerdIconsFromSettings(t *testing.T) {
	b := newFakeBackend()
	m := New(b, newFakeStore(), config.Settings{PageSize: 2, MaxColumnWidth: 40, MaxRows: 10, Icons: config.IconsNerd})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	s := screen(m)
	if !strings.Contains(s, " local") || !strings.Contains(s, " prod") {
		t.Fatalf("each connection shows its driver's logo:\n%s", s)
	}
}
