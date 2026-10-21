package ui

import (
	tea "charm.land/bubbletea/v2"
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

func esTreeAt(t *testing.T, m *Model, match func(*tree.Node) bool) {
	t.Helper()
	for _, c := range m.tree.Roots()[0].Children {
		c.Expanded = true
		for _, g := range c.Children {
			if g.Kind == tree.KindIndexFolder && match(g) == false {
				continue
			}
		}
	}
	selectKafkaNode(t, m, match)
}

func TestESDeleteIndex(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	ix := f.addIndex("orders", ordersMapping, 3)
	ix.info.PriStoreBytes = 2 << 20
	f.aliases = []es.Alias{{Name: "orders-alias", Index: "orders"}}
	pressAll(t, m, "l")
	esTreeAt(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindESIndex && n.Key == "orders" })
	pressAll(t, m, "d", "d")
	v := confirmText(t, m)
	if !strings.Contains(v, "3 docs") || !strings.Contains(v, "orders-alias") || !strings.Contains(v, "type orders to confirm") {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "orders")
	pressAll(t, m, "enter")
	if !slices.Contains(f.sent(), "DELETE orders") {
		t.Errorf("sent %v", f.sent())
	}
}

func TestESDeleteRolloverGroup(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	for _, n := range []string{"logs-app-2026.10.19", "logs-app-2026.10.20", "logs-app-2026.10.21"} {
		f.addIndex(n, "", 1)
	}
	pressAll(t, m, "l")
	esTreeAt(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindIndexFolder })
	pressAll(t, m, "d", "d")
	v := confirmText(t, m)
	if !strings.Contains(v, "logs-app-2026.10.19") || !strings.Contains(v, "type logs-app-* to confirm") {
		t.Fatalf("confirm:\n%s", v)
	}
	f.addIndex("logs-app-2026.10.22", "", 1) // made after the expansion
	typeText(t, m, "logs-app-*")
	pressAll(t, m, "enter")
	if !slices.Contains(f.sent(), "DELETE logs-app-2026.10.19,logs-app-2026.10.20,logs-app-2026.10.21") {
		t.Errorf("sent %v", f.sent())
	}
	m2, _, f2 := newESModel(t, config.Connection{Env: config.EnvProd, ReadOnly: new(bool)})
	f2.addIndex("logs-app-2026.10.19", "", 1)
	f2.addIndex("logs-app-2026.10.20", "", 1)
	pressAll(t, m2, "l")
	esTreeAt(t, m2, func(n *tree.Node) bool { return n.Kind == tree.KindIndexFolder })
	pressAll(t, m2, "d", "d")
	if m2.overlay != nil || m2.status != esProdRefusal {
		t.Errorf("prod: overlay %T status %q", m2.overlay, m2.status)
	}
}

func TestESCreateIndex(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", "", 1)
	pressAll(t, m, "l")
	esTreeAt(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindESSection && n.Section == esSecIndices })
	pressAll(t, m, "o")
	if _, ok := m.overlay.(*overlay.IndexForm); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, "fresh")
	pressAll(t, m, "ctrl+s")
	if v := confirmText(t, m); !strings.Contains(v, "fresh") {
		t.Fatalf("confirm:\n%s", v)
	}
	pressAll(t, m, "y")
	if b := f.lastBody("PUT fresh"); !strings.Contains(b, `"number_of_shards":1`) {
		t.Errorf("sent %v body %s", f.sent(), b)
	}
	if !strings.Contains(m.status, "created fresh") {
		t.Errorf("status %q", m.status)
	}
}

func TestESIndexMenu(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", "", 1)
	pressAll(t, m, "l")
	esTreeAt(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindESIndex })
	items := m.nodeMenu(m.tree.Selected())
	var labels []string
	for _, it := range items {
		labels = append(labels, it.desc)
	}
	for _, want := range []string{"Open index", "Close index…", "Refresh index", "Flush index", "Clear caches", "Force merge…", "Open document by _id…", "Delete index…"} {
		if !slices.Contains(labels, want) {
			t.Errorf("menu %v lacks %q", labels, want)
		}
	}
	pick := func(desc string) {
		for _, it := range items {
			if it.desc == desc && it.msg != nil {
				drain(t, m, func() tea.Msg { return it.msg })
				return
			}
		}
		t.Fatalf("no %q", desc)
	}
	pick("Refresh index")
	if !slices.Contains(f.sent(), "POST orders/_refresh") || m.overlay != nil {
		t.Errorf("refresh: overlay %T sent %v", m.overlay, f.sent())
	}
	pick("Close index…")
	if v := confirmText(t, m); !strings.Contains(v, "type orders to confirm") {
		t.Fatalf("close:\n%s", v)
	}
	typeText(t, m, "orders")
	pressAll(t, m, "enter")
	if !slices.Contains(f.sent(), "POST orders/_close") {
		t.Errorf("sent %v", f.sent())
	}
	pick("Force merge…")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("force merge: overlay %T", m.overlay)
	}
	pressAll(t, m, "enter")
	typeText(t, m, "orders")
	pressAll(t, m, "enter")
	if !slices.Contains(f.sent(), "POST orders/_forcemerge?max_num_segments=1") {
		t.Errorf("sent %v", f.sent())
	}
}

func TestESAliasesEdit(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", "", 1)
	f.aliases = []es.Alias{{Name: "old", Index: "orders"}}
	tab := openESIndex(t, m, "orders", targetIndex)
	press(t, m, "]", "]", "]")
	if tab.section != idxAliases {
		t.Fatalf("section %v", tab.section)
	}
	press(t, m, "o")
	typeText(t, m, "current")
	press(t, m, "enter")
	if _, ok := m.overlay.(*overlay.Choice); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	press(t, m, "enter") // the first option: not the write index
	press(t, m, "y")
	if b := f.lastBody("POST _aliases"); b != `{"actions":[{"add":{"alias":"current","index":"orders"}}]}` {
		t.Errorf("add body = %s", b)
	}
	press(t, m, "d", "d", "y")
	if b := f.lastBody("POST _aliases"); b != `{"actions":[{"remove":{"alias":"old","index":"orders"}}]}` {
		t.Errorf("remove body = %s", b)
	}
}

func TestESSettingsEdit(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", "", 1)
	tab := openESIndex(t, m, "orders", targetIndex)
	press(t, m, "]", "]")
	if tab.section != idxSettings {
		t.Fatalf("section %v", tab.section)
	}
	row := func(key string) {
		for i, s := range tab.settingList {
			if s.Key == key {
				tab.setGrid.SetCursor(i, 1)
				return
			}
		}
		t.Fatalf("no %s", key)
	}
	row("index.number_of_shards")
	press(t, m, "i")
	if m.overlay != nil || tab.status != "static setting; needs a closed index" {
		t.Errorf("static: overlay %T status %q", m.overlay, tab.status)
	}
	row("index.refresh_interval")
	press(t, m, "i")
	for range 5 {
		press(t, m, "backspace")
	}
	typeText(t, m, "30s")
	press(t, m, "enter")
	press(t, m, "space", "w")
	press(t, m, "y")
	if b := f.lastBody("PUT orders/_settings"); b != `{"index.refresh_interval":"30s"}` {
		t.Errorf("body = %s", b)
	}
	row("index.number_of_replicas")
	press(t, m, "i", "backspace")
	typeText(t, m, "0")
	press(t, m, "enter", "space", "w")
	if v := confirmText(t, m); !strings.Contains(v, "type orders to confirm") {
		t.Errorf("replicas 0:\n%s", v)
	}
}
