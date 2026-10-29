package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
	"github.com/shepherd-yun/datavim/internal/ui/uitest"

	tea "charm.land/bubbletea/v2"
)

// newESModel is a model whose only connection is the elasticsearch "search".
func newESModel(t *testing.T, cfg config.Connection) (*Model, *fakeBackend, *fakeES) {
	t.Helper()
	b := newFakeBackend()
	b.conns = nil
	if cfg.Name == "" {
		cfg.Name = "search"
	}
	f := b.addES(cfg)
	return newTestModelWith(t, b), b, f
}

// twelveIndices fills f with the indices of the tree tests.
func twelveIndices(f *fakeES) {
	for i, n := range []string{
		"orders", "users", "logs-app-2026.10.19", "logs-app-2026.10.21", "logs-app-2026.10.20",
		".kibana_1", "products", "audit", "metrics", "carts", "events", "zeta",
	} {
		ix := f.addIndex(n, "", 0)
		ix.info.Docs, ix.info.PriStoreBytes = int64(i*1000), int64((12-i)*1<<20)
	}
	f.index("users").info.Status = "close"
	f.index("orders").info.Health = "yellow"
	f.aliases = []es.Alias{{Name: "orders-alias", Index: "orders", IsWrite: true}}
}

func esSection(t *testing.T, m *Model, name string) *tree.Node {
	t.Helper()
	for _, c := range m.tree.Roots()[0].Children {
		if c.Kind == tree.KindESSection && c.Section == name {
			return c
		}
	}
	t.Fatalf("no %s section in %v", name, names(m.tree.Roots()[0].Children))
	return nil
}

func TestESTreeLoads(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	twelveIndices(f)
	f.streams = []es.DataStream{{Name: "logs-ds", Template: "logs", Indices: []string{".ds-logs-ds-2026.10.21-000001"}}}
	pressAll(t, m, "l")
	root := m.tree.Roots()[0]
	if got := names(root.Children); !slices.Equal(got, []string{"Indices (11)", "Aliases (1)", "Data Streams (1)", "Index Templates (0)", "ILM Policies (0)", "Ingest Pipelines (0)"}) {
		t.Fatalf("sections = %v", got)
	}
	if root.Detail != "[8.11.0 · green]" {
		t.Errorf("root detail = %q", root.Detail)
	}
	idx := esSection(t, m, "indices")
	got := names(idx.Children)
	if slices.Contains(got, ".kibana_1") || !slices.Contains(got, "logs-app-* (3)") {
		t.Fatalf("indices = %v", got)
	}
	var group, users, orders *tree.Node
	for _, c := range idx.Children {
		switch c.Key {
		case "logs-app-":
			group = c
		case "users":
			users = c
		case "orders":
			orders = c
		}
	}
	if group.Kind != tree.KindIndexFolder || group.Children[0].Name != "logs-app-2026.10.21" || group.Children[2].Name != "logs-app-2026.10.19" {
		t.Fatalf("group = %+v %v", group, names(group.Children))
	}
	if !strings.Contains(users.Detail, "[closed]") {
		t.Errorf("closed = %q", users.Detail)
	}
	if !orders.Warn || !strings.Contains(orders.Detail, "docs") {
		t.Errorf("yellow = %+v", orders)
	}
	if a := esSection(t, m, "aliases").Children[0]; a.Kind != tree.KindESAlias || a.Key != "orders-alias" || !strings.Contains(a.Detail, "orders") {
		t.Errorf("alias = %+v", a)
	}
	if d := esSection(t, m, "streams").Children[0]; d.Kind != tree.KindDataStream || d.Key != "logs-ds" {
		t.Errorf("stream = %+v", d)
	}
	pressAll(t, m, ".")
	if got := names(esSection(t, m, "indices").Children); !slices.Contains(got, ".kibana_1") {
		t.Errorf("with system = %v", got)
	}
	if m.connKind() != keymap.KindES {
		t.Errorf("kind = %v", m.connKind())
	}
}

func TestESTreeShowSystemParam(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{Params: map[string]string{"show_system": "true"}})
	f.addIndex(".kibana_1", "", 0)
	pressAll(t, m, "l")
	if got := names(esSection(t, m, "indices").Children); !slices.Equal(got, []string{".kibana_1"}) {
		t.Errorf("indices = %v", got)
	}
}

func TestESTreeSort(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	twelveIndices(f)
	pressAll(t, m, "l")
	first := func() string { return esSection(t, m, "indices").Children[0].Name }
	if first() != "audit" {
		t.Fatalf("by name: %v", names(esSection(t, m, "indices").Children))
	}
	pressAll(t, m, "s")
	if first() != "zeta" {
		t.Errorf("by docs: %v", names(esSection(t, m, "indices").Children))
	}
	if !strings.Contains(m.status, "docs") {
		t.Errorf("status = %q", m.status)
	}
	pressAll(t, m, "s")
	if first() != "logs-app-* (3)" { // the group's sizes add up: 10+9+8 MB
		t.Errorf("by size: %v", names(esSection(t, m, "indices").Children))
	}
	pressAll(t, m, "s")
	if first() != "audit" {
		t.Errorf("by name again: %v", names(esSection(t, m, "indices").Children))
	}
}

func TestESTreeFilter(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	twelveIndices(f)
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindESSection && n.Section == "indices" })
	pressAll(t, m, "F")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, "log")
	pressAll(t, m, "enter")
	idx := esSection(t, m, "indices")
	if got := names(idx.Children); !slices.Equal(got, []string{"logs-app-* (3)"}) || !strings.Contains(idx.Detail, "filter log") {
		t.Fatalf("filtered = %v %q", got, idx.Detail)
	}
	pressAll(t, m, "F")
	pressAll(t, m, "backspace", "backspace", "backspace")
	typeText(t, m, "*-2026.10.2?")
	pressAll(t, m, "enter")
	if got := names(esSection(t, m, "indices").Children); !slices.Equal(got, []string{"logs-app-* (2)"}) {
		t.Fatalf("pattern = %v", got)
	}
	pressAll(t, m, "f", "x")
	if got := names(esSection(t, m, "indices").Children); len(got) < 5 {
		t.Fatalf("cleared = %v", got)
	}
}

func TestESTreeNoDataStreams(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.info = es.Info{Version: "7.10.2", Distribution: "elasticsearch", Major: 7, Minor: 10}
	f.addIndex("orders", "", 1)
	pressAll(t, m, "l")
	if got := names(m.tree.Roots()[0].Children); !slices.Equal(got, []string{"Indices (1)", "Aliases (0)", "Index Templates (0)", "ILM Policies (0)", "Ingest Pipelines (0)"}) {
		t.Fatalf("sections = %v", got)
	}
}

func TestESTreeAuthPrompt(t *testing.T) {
	m, b, f := newESModel(t, config.Connection{User: "elastic"})
	f.addIndex("orders", "", 1)
	b.needPassword = true
	pressAll(t, m, "l")
	if _, ok := m.overlay.(*overlay.Password); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, "pw")
	pressAll(t, m, "enter")
	if root := m.tree.Roots()[0]; len(root.Children) != 5 {
		t.Fatalf("after the password: %v", names(root.Children))
	}
}

func TestESTreeOpenIndex(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	twelveIndices(f)
	pressAll(t, m, "l")
	esSection(t, m, "indices").Expanded = true
	esSection(t, m, "aliases").Expanded = true
	for _, tc := range []struct {
		match func(*tree.Node) bool
		want  openIndexMsg
	}{
		{func(n *tree.Node) bool { return n.Kind == tree.KindESIndex && n.Key == "orders" }, openIndexMsg{conn: "search", target: "orders", kind: targetIndex}},
		{func(n *tree.Node) bool { return n.Kind == tree.KindESAlias }, openIndexMsg{conn: "search", target: "orders-alias", kind: targetAlias}},
		{func(n *tree.Node) bool { return n.Kind == tree.KindIndexFolder }, openIndexMsg{conn: "search", target: "logs-app-*", kind: targetPattern}},
	} {
		selectKafkaNode(t, m, tc.match)
		if tc.want.kind == targetPattern {
			// enter expands a group; its menu opens it as a pattern
			if c := m.openESNode(m.tree.Selected()); c == nil || c() != tc.want {
				t.Errorf("group: %v", c)
			}
			continue
		}
		_, cmd := m.Update(uitest.Key("enter"))
		if cmd == nil {
			t.Fatal("enter did nothing")
		}
		if got := cmd(); got != tc.want {
			t.Errorf("enter = %#v, want %#v", got, tc.want)
		}
	}
}

func TestESSQLActionsRefused(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", "", 1)
	pressAll(t, m, "l", "space", "s")
	if !strings.Contains(m.status, "elasticsearch") {
		t.Errorf("status = %q", m.status)
	}
}

func TestESHasNoSQLCatalog(t *testing.T) {
	_, b, _ := newESModel(t, config.Connection{})
	if _, ok := metaKeyFor(b, "search", ""); ok {
		t.Error("an elasticsearch connection gets a SQL catalog")
	}
}

func TestLocateESTabs(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	twelveIndices(f)
	f.pipelines = map[string]string{"p": `{"processors":[]}`}
	fakeSnapshots(f)
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	locateFrom(t, m, m.openIndex("search", "orders", targetIndex), func(n *tree.Node) bool {
		return n.Kind == tree.KindESIndex && n.Key == "orders"
	})
	locateFrom(t, m, m.openIndex("search", "orders-alias", targetAlias), func(n *tree.Node) bool {
		return n.Kind == tree.KindESAlias && n.Key == "orders-alias"
	})
	locateFrom(t, m, m.openIndex("search", "logs-app-*", targetPattern), func(n *tree.Node) bool {
		return n.Kind == tree.KindIndexFolder && n.Key == "logs-app-"
	})
	locateFrom(t, m, func() tea.Msg { return esOpenObjectMsg{conn: "search", key: objKey(objPipeline, "p")} }, func(n *tree.Node) bool {
		return n.Kind == tree.KindESPipeline && n.Key == "pipeline/p"
	})

	m.setFocus(focusTree)
	openSnapshotTab(t, m, "repo1", "s1")
	locateFrom(t, m, nil, func(n *tree.Node) bool { return n.Kind == tree.KindESSnapshot && n.Key == "repo1/s1" })
	repo := m.tree.Selected().Parent()
	repo.Children, repo.Loaded = nil, false // its snapshots not read yet
	locateFrom(t, m, nil, func(n *tree.Node) bool { return n == repo })
	if !strings.Contains(m.status, "not loaded") {
		t.Errorf("status %q", m.status)
	}
}
