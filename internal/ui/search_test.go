package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/meta"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
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
	press(t, m, "ctrl+s")
	if tab, ok := m.activeTab().(*structureTab); !ok || tab.ref.Name != "users" {
		t.Fatalf("active tab = %#v", m.activeTab())
	}
}

func TestGlobalSearchJumpsToColumn(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l")
	search(t, m, "users.name")
	press(t, m, "enter")
	tab, ok := m.activeTab().(*dataTab)
	if !ok {
		t.Fatalf("active tab = %#v", m.activeTab())
	}
	if col, _, ok := tab.grid.Cell(); !ok || col.Name != "name" {
		t.Fatalf("cursor on column %q", col.Name)
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

func TestGlobalSearchScopesToTheContextConnection(t *testing.T) {
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
	if got := tables(); len(got) != 1 || got[0] != "prod" {
		t.Fatalf("prod only: %v", got)
	}
	press(t, m, "ctrl+u")
	for _, r := range "@users" {
		press(t, m, string(r))
	}
	if got := tables(); len(got) != 2 {
		t.Fatalf("@ searches every connection: %v", got)
	}
	if s := screen(m); !strings.Contains(s, "local/app") || !strings.Contains(s, "prod/app") {
		t.Fatalf("results name their connection:\n%s", s)
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
