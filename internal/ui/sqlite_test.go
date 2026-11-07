package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/meta"
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

var blobCols = []db.ColumnInfo{
	{Name: "id", DatabaseType: "INTEGER"}, {Name: "name", DatabaseType: "TEXT"}, {Name: "data", DatabaseType: "BLOB"},
}

// openBlobTab is the users table with a BLOB cell in column 2 of its first row,
// on a sqlite connection.
func openBlobTab(t *testing.T) (*Model, *fakeBackend, *dataTab) {
	t.Helper()
	b := sqliteBackend()
	b.conn.noDBs = true
	b.conn.setData("users", blobCols,
		[]db.Value{{Text: "1"}, {Text: "a"}, {Text: "x'00FF'", Blob: true}},
		[]db.Value{{Text: "2"}, {Text: "b"}, {Text: "x'00ff'"}},
	)
	m := newTestModelWith(t, b)
	selectNode(t, m, "local", "users")
	press(t, m, "enter")
	d, ok := m.activeTab().(*dataTab)
	if !ok || d.table == nil {
		t.Fatalf("users not loaded:\n%s", screen(m))
	}
	return m, b, d
}

func TestEditBlobCellRefused(t *testing.T) {
	m, _, d := openBlobTab(t)
	press(t, m, "l", "l", "i")
	if _, open := m.overlay.(*overlay.CellEditor); open {
		t.Fatal("the editor opened on a BLOB cell")
	}
	if s := ansi.Strip(screen(m)); !strings.Contains(s, "BLOB") || !strings.Contains(s, "console") {
		t.Fatalf("no explanation:\n%s", s)
	}
	if d.changes() != 0 {
		t.Fatalf("changes = %d", d.changes())
	}
	// text that merely looks like a BLOB can be edited
	press(t, m, "j", "i")
	if _, open := m.overlay.(*overlay.CellEditor); !open {
		t.Fatalf("no editor on text; overlay %T", m.overlay)
	}
}

func TestEditOtherColumnInBlobRow(t *testing.T) {
	m, _, d := openBlobTab(t)
	press(t, m, "l", "i")
	if _, open := m.overlay.(*overlay.CellEditor); !open {
		t.Fatalf("no editor; overlay %T", m.overlay)
	}
	d.Update(cellEditedMsg{tabID: d.id, table: d.table, row: 0, col: 1, value: db.Value{Text: "new"}})
	if d.changes() != 1 {
		t.Fatalf("changes = %d", d.changes())
	}
}

func TestDeleteBlobRow(t *testing.T) {
	m, _, d := openBlobTab(t)
	press(t, m, "l", "l", "d", "d")
	if d.changes() != 1 {
		t.Fatalf("changes = %d", d.changes())
	}
}

func TestBlobCellsSurviveFilter(t *testing.T) {
	// the grid gives out the value it holds, Blob included
	m, _, d := openBlobTab(t)
	press(t, m, "l", "l")
	_, v, ok := d.grid.Cell()
	if !ok || !v.Blob || v.Text != "x'00FF'" {
		t.Fatalf("cell %+v", v)
	}
	got := compareFilter(sqlx.SQLite, blobCols[2], "=", v)
	if got != `"data" = x'00FF'` {
		t.Fatalf("filter %s", got)
	}
}

// sqliteConsole is a console on a file with one table in its catalog, in INSERT
// mode.
func sqliteConsole(t *testing.T) *consoleTab {
	t.Helper()
	cat := db.Catalog{
		Namespaces: []db.Namespace{{}},
		Tables:     []db.CatalogTable{{Name: "users", Kind: db.TableKindTable}},
		Columns:    []db.CatalogColumn{{Table: db.TableRef{Name: "users"}, Name: "id", Type: "INTEGER"}},
	}
	cache := meta.NewCache()
	c := newConsoleTab(1, sqliteBackend(), newFakeStore(), cache, "local", db.Namespace{},
		config.Settings{MaxRows: 2, MaxColumnWidth: 40}, timings{})
	gen, _ := cache.Start(c.metaKey)
	cache.Finish(c.metaKey, gen, meta.NewSnapshot(c.metaKey, cat, time.Now()), nil)
	c.SetSize(80, 24)
	c.editor.EnterInsert()
	return c
}

func TestSQLiteCompletesTablesAndColumns(t *testing.T) {
	c := sqliteConsole(t)
	typeIn(t, c, "select * from us")
	if c.popup == nil || c.popup.Chosen().Label != "users" {
		t.Fatalf("table popup = %+v", c.popup)
	}
	typeIn(t, c, "enter", " where i")
	if c.popup == nil || c.popup.Chosen().Label != "id" {
		t.Fatalf("column popup = %+v", c.popup)
	}
}

// A file's one namespace has no name, so it offers nothing to complete.
func TestSQLiteOffersNoEmptyNamespace(t *testing.T) {
	c := sqliteConsole(t)
	typeIn(t, c, "select * from ", "ctrl+n")
	if c.popup == nil || c.popup.Len() != 1 || c.popup.Chosen().Label != "users" {
		t.Fatalf("popup = %+v", c.popup)
	}
}
