package ui

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

func fakeTemplates(f *fakeES) {
	f.templates = []es.Template{
		{Kind: es.TemplateComposable, Name: "logs", Body: json.RawMessage(`{"index_patterns":["logs-*"],"priority":100}`)},
		{Kind: es.TemplateComposable, Name: ".hidden", Body: json.RawMessage(`{"index_patterns":[".x*"],"priority":1}`)},
		{Kind: es.TemplateLegacy, Name: "old", Body: json.RawMessage(`{"order":2,"index_patterns":["old-*","older-*"]}`)},
		{Kind: es.TemplateComponent, Name: "base", Body: json.RawMessage(`{"template":{"settings":{"number_of_shards":"1"}}}`)},
	}
}

func templateNode(t *testing.T, m *Model, name string) {
	t.Helper()
	sec := esSection(t, m, esSecTemplates)
	sec.Expanded = true
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindESTemplate && n.Name == name })
}

func TestESTreeTemplates(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeTemplates(f)
	pressAll(t, m, "l")
	sec := esSection(t, m, esSecTemplates)
	if sec.Name != "Index Templates (3)" {
		t.Fatalf("section = %q", sec.Name)
	}
	var details []string
	for _, c := range sec.Children {
		details = append(details, c.Key+"="+c.Detail)
	}
	if !slices.Contains(details, "composable/logs=composable · logs-* · p100") || !slices.Contains(details, "legacy/old=legacy · old-*, older-* · p2") ||
		!slices.Contains(details, "component/base=component") {
		t.Errorf("children = %v", details)
	}
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n == sec })
	pressAll(t, m, ".")
	if sec := esSection(t, m, esSecTemplates); sec.Name != "Index Templates (4)" {
		t.Errorf("with system = %q", sec.Name)
	}
}

func TestESTreeTemplatesUnsupported(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", "", 1)
	for _, p := range []string{"_index_template", "_template", "_component_template"} {
		f.routes["GET "+p] = func(es.Request) es.Response { return errResp(404, "fake", "no route") }
	}
	pressAll(t, m, "l")
	if sec := esSection(t, m, esSecTemplates); sec.Err != "not supported by this cluster" {
		t.Errorf("templates = %+v", sec)
	}
	if idx := esSection(t, m, esSecIndices); idx.Name != "Indices (1)" {
		t.Errorf("indices = %q", idx.Name)
	}
}

func openTemplateTab(t *testing.T, m *Model, name string) *esTemplateTab {
	t.Helper()
	pressAll(t, m, "l")
	templateNode(t, m, name)
	pressAll(t, m, "enter")
	tab, ok := m.activeTab().(*esTemplateTab)
	if !ok {
		t.Fatalf("active tab = %T (status %q)", m.activeTab(), m.status)
	}
	return tab
}

func TestESTemplateTab(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeTemplates(f)
	tab := openTemplateTab(t, m, "logs")
	if tab.Title() != "logs · composable template" || tab.Context() != keymap.ESObject {
		t.Errorf("title %q context %s", tab.Title(), tab.Context())
	}
	if s := screen(m); !strings.Contains(s, `"priority": 100`) {
		t.Errorf("screen:\n%s", s)
	}
	text, _, ok := tab.yankText(keymap.YankRow)
	if !ok || !sameJSONText(text, `{"index_patterns":["logs-*"],"priority":100}`) {
		t.Errorf("yy = %q", text)
	}
}

func sameJSONText(a, b string) bool {
	var x, y any
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil && slices.Equal([]string{mustJSON(x)}, []string{mustJSON(y)})
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func templateEditorSaves(t *testing.T, m *Model, tab *esTemplateTab, text string) {
	t.Helper()
	path := tab.editPath
	if path == "" {
		t.Fatalf("no temp file; status %q", tab.status)
	}
	if err := writeFile(path, text); err != nil {
		t.Fatal(err)
	}
	id := tab.id
	drain(t, m, func() tea.Msg { return editDoneMsg{tabID: id, path: path} })
}

func TestESTemplateEdit(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeTemplates(f)
	tab := openTemplateTab(t, m, "logs")
	press(t, m, "space", "v")
	templateEditorSaves(t, m, tab, `{"index_patterns":["logs-*"],"priority":200}`)
	if v := confirmText(t, m); !strings.Contains(v, "update composable template logs") {
		t.Fatalf("confirm:\n%s", v)
	}
	press(t, m, "y")
	if b := f.lastBody("PUT _index_template/logs"); b != `{"index_patterns":["logs-*"],"priority":200}` {
		t.Fatalf("body = %q (sent %v)", b, f.sent())
	}
	press(t, m, "space", "v")
	templateEditorSaves(t, m, tab, "{\n  \"priority\": 100,\n  \"index_patterns\": [\"logs-*\"]\n}\n")
	if tab.status != "no changes" {
		t.Errorf("status = %q", tab.status)
	}
	n := len(f.sent())
	press(t, m, "space", "v")
	templateEditorSaves(t, m, tab, `{oops`)
	if !tab.statusE || slices.ContainsFunc(f.sent()[n:], func(s string) bool { return strings.HasPrefix(s, "PUT") }) {
		t.Errorf("bad json: status %q sent %v", tab.status, f.sent()[n:])
	}
}

func TestESTemplateDelete(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeTemplates(f)
	tt := openTemplateTab(t, m, "old")
	m.setFocus(focusTree)
	templateNode(t, m, "old")
	pressAll(t, m, "d", "d")
	typeText(t, m, "old")
	pressAll(t, m, "enter")
	if !slices.Contains(f.sent(), "DELETE _template/old") {
		t.Fatalf("sent %v", f.sent())
	}
	if slices.ContainsFunc(m.tabs, func(x tab) bool { return x == tab(tt) }) {
		t.Error("the template's tab is still open")
	}
	if n := len(f.sent()); f.sent()[n-1] != "GET _component_template" {
		t.Errorf("tree not reread: %v", f.sent())
	}
}

func TestESTemplateReadOnly(t *testing.T) {
	ro := true
	m, _, f := newESModel(t, config.Connection{ReadOnly: &ro})
	fakeTemplates(f)
	tab := openTemplateTab(t, m, "logs")
	press(t, m, "space", "v")
	if tab.status != readOnlyHint || tab.editPath != "" {
		t.Errorf("status %q", tab.status)
	}
	m.setFocus(focusTree)
	templateNode(t, m, "logs")
	pressAll(t, m, "d", "d")
	if m.overlay != nil || m.status != readOnlyHint {
		t.Errorf("overlay %T status %q", m.overlay, m.status)
	}
}

func writeFile(path, text string) error { return os.WriteFile(path, []byte(text), 0o600) }
