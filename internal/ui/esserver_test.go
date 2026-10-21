package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/history"
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
