package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

func clusterSettingsTab(t *testing.T, cfg config.Connection) (*Model, *fakeES, *esServerTab) {
	t.Helper()
	m, _, f := newESModel(t, cfg)
	f.csettings = map[string]map[string]any{
		"persistent": {"a": "1"},
		"transient":  {"b": "1"},
		"defaults":   {"c": "3", "cluster.routing.allocation.awareness.attributes": []string{}},
	}
	tab := openESServerTab(t, m)
	drain(t, m, tab.setSection(secESSettings))
	return m, f, tab
}

func settingKeys(tab *esServerTab) []string {
	var out []string
	for _, r := range tab.grids[secESSettings].Result().Rows {
		out = append(out, r[0].Text+" "+r[1].Text+"="+r[2].Text)
	}
	return out
}

func TestESClusterSettingsDefaultsHidden(t *testing.T) {
	m, _, tab := clusterSettingsTab(t, config.Connection{})
	if got := settingKeys(tab); !slices.Equal(got, []string{"persistent a=1", "transient b=1"}) {
		t.Fatalf("rows = %v", got)
	}
	press(t, m, ".")
	if got := settingKeys(tab); len(got) != 4 || got[2] != "defaults c=3" {
		t.Fatalf("with defaults = %v", got)
	}
	if s := screen(m); !strings.Contains(s, "defaults shown") {
		t.Errorf("screen:\n%s", s)
	}
}

func editSetting(t *testing.T, m *Model, value string) {
	t.Helper()
	press(t, m, "i")
	p, ok := m.overlay.(*overlay.Prompt)
	if !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	_ = p
	for range 20 {
		press(t, m, "backspace")
	}
	typeText(t, m, value)
	press(t, m, "enter")
}

func TestESClusterSettingsEdit(t *testing.T) {
	m, f, tab := clusterSettingsTab(t, config.Connection{})
	press(t, m, "d", "d") // persistent a → default
	press(t, m, "j")
	editSetting(t, m, "2") // transient b
	press(t, m, ".", "j")  // defaults: c
	editSetting(t, m, "5")
	if got := settingKeys(tab); !slices.Contains(got, "persistent c=5") || !slices.Contains(got, "persistent a=(default)") {
		t.Errorf("rows = %v", got)
	}
	press(t, m, "space", "w")
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	_ = c
	typeText(t, m, "search")
	press(t, m, "enter")
	if b := f.lastBody("PUT _cluster/settings"); b != `{"persistent":{"a":null,"c":"5"},"transient":{"b":"2"}}` {
		t.Fatalf("body = %q (sent %v)", b, f.sent())
	}
	if len(tab.clusterEdits) != 0 {
		t.Errorf("edits kept: %v", tab.clusterEdits)
	}
}

func TestESClusterSettingsArray(t *testing.T) {
	m, f, _ := clusterSettingsTab(t, config.Connection{})
	press(t, m, ".")
	press(t, m, "G") // the awareness attributes, last
	editSetting(t, m, `["zone","rack"]`)
	press(t, m, "space", "w")
	typeText(t, m, "search")
	press(t, m, "enter")
	if b := f.lastBody("PUT _cluster/settings"); b != `{"persistent":{"cluster.routing.allocation.awareness.attributes":["zone","rack"]}}` {
		t.Fatalf("body = %q", b)
	}
}

func TestESClusterSettingsDefaultRow(t *testing.T) {
	m, _, tab := clusterSettingsTab(t, config.Connection{})
	press(t, m, ".", "j", "j", "d", "d")
	if tab.status != "already the default" {
		t.Errorf("status = %q", tab.status)
	}
	press(t, m, "i")
	if p, ok := m.overlay.(*overlay.Prompt); !ok || !strings.Contains(ansiView(p), "(persistent)") {
		t.Errorf("overlay = %T", m.overlay)
	}
}

func TestESClusterSettingsReadOnly(t *testing.T) {
	ro := true
	m, f, tab := clusterSettingsTab(t, config.Connection{ReadOnly: &ro})
	press(t, m, "d", "d", "space", "w")
	if m.overlay != nil || tab.status != readOnlyHint || slices.ContainsFunc(f.sent(), func(s string) bool { return strings.HasPrefix(s, "PUT") }) {
		t.Errorf("overlay %T status %q sent %v", m.overlay, tab.status, f.sent())
	}
}

func TestESClusterSettingsTickKeepsEdits(t *testing.T) {
	m, _, tab := clusterSettingsTab(t, config.Connection{})
	press(t, m, "d", "d")
	drain(t, m, tab.Handle(keymap.Refresh))
	if got := settingKeys(tab); !slices.Contains(got, "persistent a=(default)") || len(tab.clusterEdits) != 1 {
		t.Errorf("rows %v edits %v", got, tab.clusterEdits)
	}
	press(t, m, "[", "]")
	if len(tab.clusterEdits) != 1 {
		t.Errorf("edits lost on a section switch")
	}
}

func ansiView(o overlay.Overlay) string { return ansi.Strip(o.View(100, 30)) }
