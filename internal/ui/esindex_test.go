package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
)

const ordersMapping = `{"properties":{
  "order_no":{"type":"long"},
  "status":{"type":"keyword"},
  "title":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "user":{"properties":{"name":{"type":"keyword"}}},
  "@timestamp":{"type":"date"}
}}`

// openESIndex opens the Index tab of target on m's "search" connection.
func openESIndex(t *testing.T, m *Model, target string, kind targetKind) *esIndexTab {
	t.Helper()
	drain(t, m, func() tea.Msg { return openIndexMsg{conn: "search", target: target, kind: kind} })
	tab, ok := m.activeTab().(*esIndexTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	return tab
}

func TestESIndexSections(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	ix := f.addIndex("orders", ordersMapping, 3)
	ix.info.Primaries, ix.info.Replicas, ix.info.Docs, ix.info.StoreBytes = 5, 1, 8812301, 4400000000
	ix.info.Created = time.Date(2026, 3, 2, 12, 0, 0, 0, time.Local)
	tab := openESIndex(t, m, "orders", targetIndex)
	if tab.section != idxDocuments || tab.Title() != "orders" {
		t.Fatalf("section %v title %q", tab.section, tab.Title())
	}
	if h := tab.header(); h != "● green · 5p / 1r · 8,812,301 docs · 4.1 GB · created 2026-03-02" {
		t.Errorf("header = %q", h)
	}
	press(t, m, "]")
	if tab.section != idxMapping || tab.mapGrid.Result() == nil || len(tab.mapGrid.Result().Rows) != 6 {
		t.Fatalf("mapping: %v %+v", tab.section, tab.mapGrid.Result())
	}
	if !strings.Contains(screen(m), "Mapping") || !strings.Contains(screen(m), "title.keyword") {
		t.Errorf("screen:\n%s", screen(m))
	}
	press(t, m, "]")
	if tab.section != idxSettings || tab.setGrid.Result() == nil {
		t.Fatalf("settings: %v", tab.section)
	}
	if !strings.Contains(screen(m), "index.number_of_shards") {
		t.Errorf("screen:\n%s", screen(m))
	}
	press(t, m, "]", "]")
	if tab.section != idxStats || tab.statsGrid.Result() == nil {
		t.Fatalf("stats: %v", tab.section)
	}
	press(t, m, "]")
	if tab.section != idxStats {
		t.Errorf("past the end: %v", tab.section)
	}
	press(t, m, "[", "[", "[", "[", "[")
	if tab.section != idxDocuments {
		t.Errorf("back: %v", tab.section)
	}
	// the same index again: the same tab
	n := len(m.tabs)
	openESIndex(t, m, "orders", targetIndex)
	if len(m.tabs) != n {
		t.Errorf("tabs %d → %d", n, len(m.tabs))
	}
}

func TestESIndexEmptyMapping(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("fresh", "", 0)
	tab := openESIndex(t, m, "fresh", targetIndex)
	press(t, m, "]")
	if tab.section != idxMapping || !strings.Contains(screen(m), "no fields") {
		t.Errorf("screen:\n%s", screen(m))
	}
}

func TestESIndexAlias(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	for _, n := range []string{"orders-1", "orders-2", "orders-3"} {
		f.addIndex(n, ordersMapping, 2)
		f.aliases = append(f.aliases, es.Alias{Name: "orders", Index: n})
	}
	tab := openESIndex(t, m, "orders", targetAlias)
	if h := tab.header(); !strings.HasPrefix(h, "alias → 3 indices · ") || !strings.Contains(h, "6 docs") {
		t.Errorf("header = %q", h)
	}
	press(t, m, "]", "]", "]")
	if tab.section != idxAliases || len(tab.aliasGrid.Result().Rows) != 3 {
		t.Fatalf("aliases %v %+v", tab.section, tab.aliasGrid.Result())
	}
	press(t, m, "]")
	if rows := tab.statsGrid.Result().Rows; len(rows) != 3 || rows[0][0].Text != "orders-1" {
		t.Errorf("stats rows = %+v", rows)
	}
}

func TestESIndexMappingError(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 1)
	f.routes["GET orders/_mapping"] = func(es.Request) es.Response {
		return errResp(403, "security_exception", "action [indices:admin/mappings/get] is unauthorized")
	}
	tab := openESIndex(t, m, "orders", targetIndex)
	press(t, m, "]")
	if !strings.Contains(screen(m), "permission denied") {
		t.Errorf("mapping screen:\n%s", screen(m))
	}
	press(t, m, "]")
	if s := screen(m); strings.Contains(s, "permission denied") || tab.setGrid.Result() == nil {
		t.Errorf("settings screen:\n%s", s)
	}
}

func TestESIndexYankMapping(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 1)
	clip := captureClipboard(t)
	openESIndex(t, m, "orders", targetIndex)
	press(t, m, "]", "y", "y")
	if !strings.Contains(*clip, `"order_no"`) || !strings.Contains(*clip, "\n  ") {
		t.Errorf("clipboard = %q", *clip)
	}
}
