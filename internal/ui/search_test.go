package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/meta"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

// search opens <space><space> and types query.
func search(t *testing.T, m *Model, query string) {
	t.Helper()
	press(t, m, "space", "space")
	if _, ok := m.overlay.(*overlay.Search); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	for _, r := range query {
		press(t, m, string(r))
	}
}

func TestGlobalSearchOpensTablesAndStructures(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l")
	search(t, m, "users")
	if s := screen(m); !strings.Contains(s, "table    users") {
		t.Fatalf("screen:\n%s", s)
	}
	press(t, m, "enter")
	if tab, ok := m.activeTab().(*dataTab); !ok || tab.ref.Name != "users" || tab.ref.Database != "app" {
		t.Fatalf("active tab = %#v", m.activeTab())
	}
	search(t, m, "users")
	press(t, m, "ctrl+o")
	if tab, ok := m.activeTab().(*structureTab); !ok || tab.ref.Name != "users" {
		t.Fatalf("active tab = %#v", m.activeTab())
	}
}

func TestGlobalSearchJumpsToColumn(t *testing.T) {
	for _, q := range []string{"users.name", "app.users.name", "ap.usr.nam"} {
		m, _ := newTestModel(t)
		press(t, m, "l")
		search(t, m, q)
		press(t, m, "enter")
		tab, ok := m.activeTab().(*dataTab)
		if !ok {
			t.Fatalf("%q: active tab = %#v", q, m.activeTab())
		}
		if col, _, ok := tab.grid.Cell(); !ok || col.Name != "name" || tab.ref.Name != "users" {
			t.Fatalf("%q: cursor on %s.%s", q, tab.ref.Name, col.Name)
		}
	}
}

func TestGlobalSearchShowsRoutineDefinition(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l")
	search(t, m, "add_one")
	press(t, m, "enter")
	if s := screen(m); !strings.Contains(s, "CREATE FUNCTION add_one") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestGlobalSearchRevealsDatabases(t *testing.T) {
	m, _ := newTestModel(t)
	drain(t, m, m.ensureMeta("local", "")) // catalog loaded, tree not expanded yet
	search(t, m, "app")
	press(t, m, "enter")
	if n := m.tree.Selected(); m.focus != focusTree || n == nil || n.Name != "app" {
		t.Fatalf("focus %v, selected %+v", m.focus, n)
	}
}

func TestGlobalSearchSpansEveryConnectionAndAtScopesToTheContext(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l", "G") // load local, then select prod
	search(t, m, "users")
	tables := func() []string {
		var out []string
		for _, h := range m.searchHits {
			if h.Kind == meta.ItemTable && h.Name == "users" {
				out = append(out, h.Conn)
			}
		}
		return out
	}
	if got := tables(); len(got) != 2 {
		t.Fatalf("every connection by default: %v", got)
	}
	if s := screen(m); !strings.Contains(s, "local/app") || !strings.Contains(s, "prod/app") {
		t.Fatalf("results name their connection:\n%s", s)
	}
	press(t, m, "ctrl+u")
	for _, r := range "@users" {
		press(t, m, string(r))
	}
	if got := tables(); len(got) != 1 || got[0] != "prod" {
		t.Fatalf("@ searches prod only: %v", got)
	}
}

func TestGlobalSearchNotesMetadataProblems(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.mu.Lock()
	b.conn.catalogErr = errors.New("permission denied")
	b.conn.mu.Unlock()
	press(t, m, "l")
	search(t, m, "")
	if s := screen(m); !strings.Contains(s, "metadata of local failed") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestGlobalSearchDropsStaleResultsAndTakesPastes(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l")
	search(t, m, "u")
	m.Update(searchResultsMsg{gen: 0, hits: []meta.Hit{{Item: meta.Item{Kind: meta.ItemTable, Path: "stale"}}}})
	if strings.Contains(screen(m), "stale") {
		t.Fatal("results of an older query are dropped")
	}
	_, cmd := m.Update(tea.PasteMsg{Content: "sers"})
	drain(t, m, cmd)
	if s := screen(m); !strings.Contains(s, "table    users") {
		t.Fatalf("a paste runs the search:\n%s", s)
	}
}

func TestGlobalSearchLoadsEveryConnection(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "G") // prod selected; nothing loaded yet
	search(t, m, "users")
	conns := map[string]bool{}
	for _, h := range m.searchHits {
		if h.Kind == meta.ItemTable && h.Name == "users" {
			conns[h.Conn] = true
		}
	}
	if !conns["local"] || !conns["prod"] {
		t.Fatalf("the search loads the catalogs of every connection: %v", conns)
	}
}

func TestGlobalSearchNotesLoadingConnections(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "G", "space")
	_, cmd := m.Update(uitest.Key("space")) // starts loading prod and local
	if s := screen(m); !strings.Contains(s, "loading metadata of prod… · loading metadata of 1 connection…") {
		t.Fatalf("screen:\n%s", s)
	}
	drain(t, m, cmd)
	if s := screen(m); strings.Contains(s, "loading metadata") {
		t.Fatalf("note stays after the loads:\n%s", s)
	}
}

func TestSearchConsolesJumpsToTheLine(t *testing.T) {
	m, _ := newTestModel(t)
	drain(t, m, m.openConsole("local", db.Namespace{Database: "app"}))
	first := m.activeTab().(*consoleTab)
	first.editor.SetValue("select *\nfrom users\nwhere name = 'x'")
	drain(t, m, m.openConsole("prod", db.Namespace{Database: "app"}))
	m.activeTab().(*consoleTab).editor.SetValue("update users set NAME = 1")
	press(t, m, "esc")
	search(t, m, "name")
	press(t, m, "tab")
	if len(m.searchPicks) != 2 {
		t.Fatalf("a line of each console: %d", len(m.searchPicks))
	}
	s := screen(m)
	for _, want := range []string{"console  where name = 'x'", "line 3", "update users set NAME = 1"} {
		if !strings.Contains(s, want) {
			t.Fatalf("screen lacks %q:\n%s", want, s)
		}
	}
	press(t, m, "enter")
	if m.activeTab() != first || m.focus != focusTabs {
		t.Fatalf("active tab = %#v", m.activeTab())
	}
	if line, col := first.editor.Cursor(); line != 2 || col != 6 {
		t.Fatalf("cursor at %d:%d, want on the match", line, col)
	}
}

func TestSearchHistoryInsertsTheStatement(t *testing.T) {
	m, _ := newTestModel(t)
	m.store.(*fakeStore).entries = []history.Entry{
		{Time: time.Now(), Conn: "local", Database: "app", SQL: "select 7"},
		{Time: time.Now(), Conn: "prod", Database: "app", SQL: "SELECT name\n  FROM t", Err: "boom"},
	}
	search(t, m, "")
	press(t, m, "tab", "tab")
	if s := screen(m); !strings.Contains(s, "2 recent statements") || !strings.Contains(s, "SELECT name FROM t") {
		t.Fatalf("the recent statements, on one line:\n%s", s)
	}
	typeText(t, m, "@sel")
	if len(m.searchPicks) != 1 {
		t.Fatalf("@ keeps the statements of local: %d", len(m.searchPicks))
	}
	press(t, m, "ctrl+u")
	typeText(t, m, "NAME")
	if s := screen(m); !strings.Contains(s, "1 matches") || !strings.Contains(s, "failed") {
		t.Fatalf("matches ignore case and note failures:\n%s", s)
	}
	press(t, m, "enter")
	c, ok := m.activeTab().(*consoleTab)
	if !ok || c.conn != "prod" || c.editor.Value() != "SELECT name\n  FROM t" {
		t.Fatalf("console = %#v", m.activeTab())
	}
}
