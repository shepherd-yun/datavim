package ui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// editSchemaTo opens the shown version in "$EDITOR" and saves text: the
// test writes the temp file the editor would have.
func editSchemaTo(t *testing.T, m *Model, st *schemaTab, text string) {
	t.Helper()
	pressAll(t, m, "space", "v")
	if st.editPath == "" {
		t.Fatalf("no temp file; status %q", st.status)
	}
	if text != "" {
		if err := os.WriteFile(st.editPath, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	id, path := st.id, st.editPath
	drainAll(t, m, func() tea.Msg { return editDoneMsg{tabID: id, path: path} })
}

const orderV4 = `{"type":"record","name":"Order","fields":[{"name":"id","type":"long"},{"name":"note","type":"string","default":""},{"name":"total","type":"long","default":0},{"name":"tax","type":"long","default":0}]}`

func TestRegisterSchemaCompatible(t *testing.T) {
	m, _, reg := schemaModel(t, config.Connection{})
	st := openSchemaTab(t, m, "orders-value", -1)
	editSchemaTo(t, m, st, orderV4)
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, st.status)
	}
	if v := ansi.Strip(c.View(120, 20)); !strings.Contains(v, "compatible with v3 (BACKWARD)") {
		t.Fatalf("confirm:\n%s", v)
	}
	pressAll(t, m, "y")
	drainAll(t, m, nil)
	if got := reg.callsOf("Register"); len(got) != 1 {
		t.Fatalf("Register calls = %v", got)
	}
	if !strings.Contains(st.header(), "v4 of 4") {
		t.Fatalf("header = %q", st.header())
	}
}

func TestRegisterSchemaIncompatible(t *testing.T) {
	m, _, reg := schemaModel(t, config.Connection{})
	reg.checkOK, reg.checkMsgs = false, []string{"field id removed"}
	st := openSchemaTab(t, m, "orders-value", -1)
	editSchemaTo(t, m, st, `{"type":"record","name":"Order","fields":[]}`)
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, st.status)
	}
	if v := ansi.Strip(c.View(120, 20)); !strings.Contains(v, "NOT compatible: field id removed") {
		t.Fatalf("confirm:\n%s", v)
	}
	pressAll(t, m, "y")
	drainAll(t, m, nil)
	if got := reg.callsOf("Register"); len(got) != 0 {
		t.Fatalf("Register calls = %v", got)
	}
}

func TestRegisterSchemaUnchanged(t *testing.T) {
	m, _, reg := schemaModel(t, config.Connection{})
	st := openSchemaTab(t, m, "orders-value", -1)
	editSchemaTo(t, m, st, "")
	if st.status != "no changes" || len(reg.callsOf("Check")) != 0 {
		t.Fatalf("status %q, checks %v", st.status, reg.callsOf("Check"))
	}
}

func TestSetCompatibility(t *testing.T) {
	m, _, reg := schemaModel(t, config.Connection{})
	st := openSchemaTab(t, m, "orders-value", -1)
	pressAll(t, m, "c")
	if _, ok := m.overlay.(*overlay.Choice); !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, st.status)
	}
	// BACKWARD (the current level) is first; FULL is the 5th of the others
	pressAll(t, m, "j", "j", "j", "j", "enter")
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	if v := ansi.Strip(c.View(120, 20)); !strings.Contains(v, "orders-value: BACKWARD → FULL") {
		t.Fatalf("confirm:\n%s", v)
	}
	pressAll(t, m, "y")
	drainAll(t, m, nil)
	if got := reg.callsOf("SetCompatibility"); len(got) != 1 || got[0] != "SetCompatibility orders-value FULL" {
		t.Fatalf("calls = %v", got)
	}
	if !strings.Contains(st.header(), "compat FULL") || strings.Contains(st.header(), "(global)") {
		t.Fatalf("header = %q", st.header())
	}
}

func TestSoftDeleteVersion(t *testing.T) {
	m, _, reg := schemaModel(t, config.Connection{})
	st := openSchemaTab(t, m, "orders-value", -1)
	pressAll(t, m, "]", "g", "g", "j", "d", "d")
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, st.status)
	}
	if v := flatView(c); !strings.Contains(v, "Soft-delete orders-value v2? Messages written with it can still be read (id 11 stays).") || confirmExpect(c) != "orders-value" {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "orders-value")
	pressAll(t, m, "enter")
	drainAll(t, m, nil)
	if got := reg.callsOf("DeleteVersion"); len(got) != 1 || got[0] != "DeleteVersion orders-value 2" {
		t.Fatalf("calls = %v", got)
	}
	if len(st.versions) != 2 {
		t.Fatalf("versions after the delete = %d", len(st.versions))
	}
}

func TestSoftDeleteFromTree(t *testing.T) {
	m, _, reg := registryTree(t)
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Section == secSchemas && n.Kind == tree.KindKafkaSection })
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindSchemaSubject && n.Key == "orders-value" })
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindSchemaVersion && n.Name == "v1" })
	pressAll(t, m, "d", "d")
	if _, ok := m.overlay.(*overlay.Confirm); !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, m.status)
	}
	typeText(t, m, "orders-value")
	pressAll(t, m, "enter")
	if got := reg.callsOf("DeleteVersion"); len(got) != 1 || got[0] != "DeleteVersion orders-value 1" {
		t.Fatalf("calls = %v", got)
	}
}

func TestSchemaWritesReadOnly(t *testing.T) {
	on := true
	m, _, reg := schemaModel(t, config.Connection{ReadOnly: &on})
	st := openSchemaTab(t, m, "orders-value", -1)
	for _, keys := range [][]string{{"space", "v"}, {"c"}, {"]", "d", "d"}} {
		pressAll(t, m, keys...)
		if m.overlay != nil || st.status != readOnlyHint {
			t.Fatalf("%v: overlay %T status %q", keys, m.overlay, st.status)
		}
	}
	if len(reg.calls) > 0 && len(reg.callsOf("Register"))+len(reg.callsOf("SetCompatibility"))+len(reg.callsOf("DeleteVersion")) > 0 {
		t.Fatalf("calls = %v", reg.calls)
	}
}
