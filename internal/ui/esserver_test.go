package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/history"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

func TestESServerOverview(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.health = es.Health{Status: "yellow", Nodes: 3, DataNodes: 2, ActivePrimary: 10, Active: 17, Unassigned: 3, PendingTasks: 1}
	f.addIndex("orders", "", 5)
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindConn })
	press(t, m, "space", "i")
	tab, ok := m.activeTab().(*esServerTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	s := screen(m)
	for _, want := range []string{"8.11.0", "elasticsearch", "yellow", "3 unassigned shards", "nodes", "3 (2 data)", "pending tasks"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	tab.Hidden()
	n := len(f.sent())
	id := tab.id
	drain(t, m, func() tea.Msg { return esServerTickMsg{tabID: id, gen: tab.gen} })
	if len(f.sent()) != n {
		t.Errorf("hidden but read %v", f.sent()[n:])
	}
}

func TestSearchES(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", "", 1)
	f.addIndex("users", "", 1)
	f.aliases = []es.Alias{{Name: "orders-alias", Index: "orders"}}
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	search(t, m, "ord")
	s := screen(m)
	if !strings.Contains(s, "orders") || !strings.Contains(s, "orders-alias") || !strings.Contains(s, "alias") || strings.Contains(s, "users") {
		t.Fatalf("screen:\n%s", s)
	}
	pressAll(t, m, "enter")
	if tab, ok := m.activeTab().(*esIndexTab); !ok || tab.target != "orders" || tab.kind != targetIndex {
		t.Fatalf("active tab = %#v", m.activeTab())
	}
}

func TestRecentES(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", "", 1)
	f.aliases = []es.Alias{{Name: "orders-alias", Index: "orders"}}
	openESIndex(t, m, "orders-alias", targetAlias)
	drain(t, m, m.openESConsole("search"))
	for len(m.tabs) > 0 {
		drain(t, m, m.closeTab(0))
	}
	st := m.store.(*fakeStore)
	var kinds []string
	for _, r := range st.recents {
		kinds = append(kinds, r.Kind+":"+r.Name+":"+r.Database)
	}
	if !strings.Contains(strings.Join(kinds, " "), history.RecentESIndex+":orders-alias:alias") {
		t.Fatalf("recents %v", kinds)
	}
	for _, r := range slices.Clone(st.recents) {
		drain(t, m, m.openRecent(r))
	}
	var idx *esIndexTab
	var console bool
	for _, tb := range m.tabs {
		switch x := tb.(type) {
		case *esIndexTab:
			idx = x
		case *esConsoleTab:
			console = true
		}
	}
	if idx == nil || idx.kind != targetAlias || !console {
		t.Errorf("tabs %T idx %v console %v recents %v", m.tabs[0], idx != nil, console, kinds)
	}
}

// openESServerTab opens the cluster tab of the "search" connection.
func openESServerTab(t *testing.T, m *Model) *esServerTab {
	t.Helper()
	drain(t, m, m.openESServer("search"))
	tab, ok := m.activeTab().(*esServerTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	return tab
}

func twoNodes(f *fakeES) {
	f.nodes = []es.Node{
		{ID: "n1", Name: "es-1", IP: "10.0.0.1", Roles: "dimr", Master: true, HeapPct: 40, RAMPct: 50, CPUPct: 3, Load1m: 0.5, DiskUsedPct: 30, DiskUsed: 30 << 30, DiskTotal: 100 << 30, Version: "8.11.0"},
		{ID: "n2", Name: "es-2", IP: "10.0.0.2", Roles: "di", HeapPct: 90, RAMPct: 50, CPUPct: 3, Load1m: 0.5, DiskUsedPct: 88, DiskUsed: 88 << 30, DiskTotal: 100 << 30, Version: "8.11.0"},
	}
}

func TestESServerSections(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	twoNodes(f)
	tab := openESServerTab(t, m)
	n := len(f.sent())
	press(t, m, "]")
	if tab.section != secESNodes {
		t.Fatalf("section = %d", tab.section)
	}
	got := f.sent()[n:]
	for _, want := range []string{"GET _cluster/health", "GET _cat/nodes", "GET _cat/allocation", "GET _cluster/settings"} {
		if !slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, want) }) {
			t.Errorf("Nodes read %v, lacks %s", got, want)
		}
	}
	for _, s := range got {
		if strings.HasPrefix(s, "GET _cat/shards") || s == "GET " || strings.HasPrefix(s, "GET _cluster/stats") {
			t.Errorf("Nodes read %s", s)
		}
	}
	if s := screen(m); !strings.Contains(s, "es-1") || !strings.Contains(s, "HEAP %") || !strings.Contains(s, "★") {
		t.Errorf("screen:\n%s", s)
	}
	// the watermark is read once
	n = len(f.sent())
	drain(t, m, tab.Handle(keymap.Refresh))
	for _, s := range f.sent()[n:] {
		if strings.HasPrefix(s, "GET _cluster/settings") {
			t.Errorf("settings read again: %v", f.sent()[n:])
		}
	}
	for _, want := range []eserverSection{secESShards, secESAllocation, secESTasks, secESPending, secESSettings} {
		press(t, m, "]")
		if tab.section != want {
			t.Fatalf("section = %d, want %d", tab.section, want)
		}
	}
	if tab.Context() != keymap.ESClusterSettings {
		t.Errorf("context = %s", tab.Context())
	}
	press(t, m, "[")
	if tab.section != secESPending || tab.Context() != keymap.ESServer {
		t.Errorf("section %d context %s", tab.section, tab.Context())
	}
	if s := screen(m); !strings.Contains(s, "no pending cluster tasks") {
		t.Errorf("screen:\n%s", s)
	}
	f.pending = []es.PendingTask{{Order: 3, Priority: "HIGH", Source: "shard-started", Waiting: "5ms"}}
	drain(t, m, tab.Handle(keymap.Refresh))
	if s := screen(m); !strings.Contains(s, "shard-started") || !strings.Contains(s, "PRIORITY") {
		t.Errorf("screen:\n%s", s)
	}
	press(t, m, "[", "[")
	if s := screen(m); !strings.Contains(s, "DISK AVAIL") || !strings.Contains(s, "es-2") {
		t.Errorf("allocation screen:\n%s", s)
	}
}

func TestESServerNodesWarn(t *testing.T) {
	for _, tc := range []struct {
		watermark  string
		heap, disk int
		warn       bool
	}{
		{"", 90, 10, true},
		{"85%", 10, 88, true},
		{"", 10, 88, false},
		{"85%", 10, 80, false},
	} {
		m, _, f := newESModel(t, config.Connection{})
		f.nodes = []es.Node{{ID: "n1", Name: "es-1", HeapPct: tc.heap, DiskUsedPct: float64(tc.disk), DiskUsed: int64(tc.disk) << 30, DiskTotal: 100 << 30}}
		if tc.watermark != "" {
			f.csettings = map[string]map[string]any{"defaults": {"cluster.routing.allocation.disk.watermark.high": tc.watermark}}
		}
		tab := openESServerTab(t, m)
		drain(t, m, tab.setSection(secESNodes))
		if got := tab.nodeMark(0, 0) == grid.MarkError; got != tc.warn {
			t.Errorf("%+v: warn = %v", tc, got)
		}
	}
}

func TestESServerOverviewJump(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.health.Status = "yellow"
	tab := openESServerTab(t, m)
	press(t, m, "enter")
	if tab.section != secESShards {
		t.Errorf("yellow: section = %d", tab.section)
	}
	f.health.Status = "green"
	drain(t, m, tab.setSection(secESOverview))
	press(t, m, "enter")
	if tab.section != secESOverview {
		t.Errorf("green: section = %d", tab.section)
	}
}

func TestESServerHiddenStopsSections(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	twoNodes(f)
	tab := openESServerTab(t, m)
	drain(t, m, tab.setSection(secESNodes))
	tab.Hidden()
	n := len(f.sent())
	id := tab.id
	drain(t, m, func() tea.Msg { return esServerTickMsg{tabID: id, gen: tab.gen} })
	if len(f.sent()) != n {
		t.Errorf("hidden but read %v", f.sent()[n:])
	}
}
