package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
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
