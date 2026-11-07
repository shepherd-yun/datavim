package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

func sqliteBackend() *fakeBackend {
	b := newFakeBackend()
	b.conns = []config.Connection{{Name: "local", Driver: "sqlite", Database: "/tmp/a.db", Env: config.EnvDev}}
	return b
}

// sqliteTree is a model with a sqlite connection, fakeConn behind it having no
// databases and no schemas.
func sqliteTree(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	b := sqliteBackend()
	b.conn.noDBs = true
	m := newTestModelWith(t, b)
	m.setFocus(focusTree)
	return m, b
}

func TestSQLiteTreeTwoLevels(t *testing.T) {
	m, _ := sqliteTree(t)
	n := m.tree.Selected()
	if n.Kind != tree.KindConn {
		t.Fatalf("selected %v", n.Kind)
	}
	press(t, m, "enter")
	if len(n.Children) == 0 {
		t.Fatalf("no children; status %q", m.status)
	}
	for _, c := range n.Children {
		if c.Kind != tree.KindTable && c.Kind != tree.KindView {
			t.Fatalf("child %q is a %v", c.Name, c.Kind)
		}
		if c.NS != (db.Namespace{}) {
			t.Fatalf("child %q has namespace %+v", c.Name, c.NS)
		}
	}
}

func TestSQLiteConnMenu(t *testing.T) {
	m, _ := sqliteTree(t)
	var descs []string
	for _, it := range m.nodeMenu(m.tree.Selected()) {
		descs = append(descs, it.desc)
	}
	all := strings.Join(descs, "|")
	for _, want := range []string{"Open console", "Create table from file…", "Mark for compare", "Compare with the marked one", "Edit connection"} {
		if !slices.Contains(descs, want) {
			t.Errorf("missing %q in %s", want, all)
		}
	}
	for _, no := range []string{"Server status", "Enter password"} {
		if slices.Contains(descs, no) {
			t.Errorf("%q in %s", no, all)
		}
	}
}

func TestSQLiteCompareImportAtConn(t *testing.T) {
	m, _ := sqliteTree(t)
	n := m.tree.Selected()
	side, problem := m.compareSideOf(n)
	if problem != "" || side.isTable() || side.ref != (db.TableRef{}) {
		t.Fatalf("compare: %+v %q", side, problem)
	}
	m.importFromNode()
	p, ok := m.overlay.(*overlay.Prompt)
	if !ok {
		t.Fatalf("overlay %T, status %q", m.overlay, m.status)
	}
	if s := ansi.Strip(p.View(80, 20)); !strings.Contains(s, "Create a table in local") {
		t.Fatalf("view:\n%s", s)
	}

	// other engines keep their answer on a connection node
	b := newFakeBackend()
	other := newTestModelWith(t, b)
	other.setFocus(focusTree)
	if _, problem := other.compareSideOf(other.tree.Selected()); problem == "" {
		t.Fatal("mysql compares at the connection")
	}
}

func TestSQLiteCopyQualifiedName(t *testing.T) {
	m, _ := sqliteTree(t)
	press(t, m, "enter")
	n := m.tree.Selected().Children[0]
	if got := m.qualifiedName(n); got != n.Name && got != sqlx.QuoteIfNeeded(n.Name, sqlx.SQLite) {
		t.Fatalf("got %q for %q", got, n.Name)
	}
}

func TestServerStatusHiddenForSQLite(t *testing.T) {
	m := newTestModelWith(t, sqliteBackend())
	m.setFocus(focusTree)
	if slices.ContainsFunc(m.nodeMenu(m.tree.Selected()), func(it menuItem) bool { return strings.Contains(it.desc, "status") }) {
		t.Fatal("the connection menu offers server status")
	}
	press(t, m, "space", "i")
	if _, onServer := m.activeTab().(*sqlServerTab); onServer {
		t.Fatal("server tab opened")
	}
	if !strings.Contains(m.status, "SQLite has no server status") {
		t.Fatalf("status %q", m.status)
	}
}
