package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
)

const (
	orderV1 = `{"type":"record","name":"Order","fields":[{"name":"id","type":"long"}]}`
	orderV2 = `{"type":"record","name":"Order","fields":[{"name":"id","type":"long"},{"name":"note","type":"string","default":""}]}`
	orderV3 = `{"type":"record","name":"Order","fields":[{"name":"id","type":"long"},{"name":"note","type":"string","default":""},{"name":"total","type":"long","default":0}]}`
)

// schemaModel is a kafka model whose registry has orders-value v1..v3 (ids 10..12).
func schemaModel(t *testing.T, cfg config.Connection) (*Model, *fakeBackend, *fakeRegistry) {
	t.Helper()
	m, b, _ := newKafkaModel(t, withRegistry(cfg))
	b.registry = newFakeRegistry()
	b.registry.add("orders-value", 1, 10, "AVRO", orderV1)
	b.registry.add("orders-value", 2, 11, "AVRO", orderV2)
	b.registry.add("orders-value", 3, 12, "AVRO", orderV3)
	return m, b, b.registry
}

func openSchemaTab(t *testing.T, m *Model, subject string, version int) *schemaTab {
	t.Helper()
	drainAll(t, m, m.openSchema("events", subject, version))
	st, ok := m.activeTab().(*schemaTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	return st
}

func TestSchemaTabShows(t *testing.T) {
	m, _, _ := schemaModel(t, config.Connection{})
	st := openSchemaTab(t, m, "orders-value", -1)
	v := ansi.Strip(st.View(true))
	if !strings.Contains(v, "orders-value · v3 of 3 · id 12 · AVRO · compat BACKWARD (global)") {
		t.Fatalf("view:\n%s", v)
	}
	if !strings.Contains(v, `  "type": "record",`) {
		t.Fatalf("the schema is not indented:\n%s", v)
	}
}

func TestSchemaTabVersion(t *testing.T) {
	m, _, _ := schemaModel(t, config.Connection{})
	st := openSchemaTab(t, m, "orders-value", -1)
	pressAll(t, m, "]", "g", "g", "enter")
	drainAll(t, m, nil)
	if st.section != schSchema || !strings.Contains(ansi.Strip(st.View(true)), "v1 of 3 · id 10") {
		t.Fatalf("section %d view:\n%s", st.section, ansi.Strip(st.View(true)))
	}
}

func TestSchemaDiff(t *testing.T) {
	m, _, _ := schemaModel(t, config.Connection{})
	st := openSchemaTab(t, m, "orders-value", -1)
	pressAll(t, m, "]", "g", "g", "m", "j", "C")
	drainAll(t, m, nil)
	if st.section != schDiff {
		t.Fatalf("section = %d", st.section)
	}
	v := ansi.Strip(st.View(true))
	found := false
	for _, l := range strings.Split(v, "\n") {
		if strings.HasPrefix(strings.TrimLeft(l, "│ "), "+") && strings.Contains(l, "note") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no + line with note:\n%s", v)
	}
	// no mark: the cursor's version against the latest
	pressAll(t, m, "[", "G", "C")
	drainAll(t, m, nil)
	if v := ansi.Strip(st.View(true)); !strings.Contains(v, "no differences") {
		t.Fatalf("latest against itself:\n%s", v)
	}
}

func TestSchemaTabReused(t *testing.T) {
	m, _, _ := schemaModel(t, config.Connection{})
	openSchemaTab(t, m, "orders-value", 1)
	st := openSchemaTab(t, m, "orders-value", 2)
	if len(m.tabs) != 1 {
		t.Fatalf("%d tabs", len(m.tabs))
	}
	if !strings.Contains(ansi.Strip(st.View(true)), "v2 of 3") {
		t.Fatalf("view:\n%s", ansi.Strip(st.View(true)))
	}
}

func TestSchemaTabCloses(t *testing.T) {
	m, _, _ := schemaModel(t, config.Connection{})
	openSchemaTab(t, m, "orders-value", -1)
	drainAll(t, m, m.closeTab(m.active))
	if len(m.tabs) != 0 {
		t.Fatal("the tab stays")
	}
}
