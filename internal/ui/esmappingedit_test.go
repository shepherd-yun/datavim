package ui

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
)

const statusMapping = `{"properties":{"status":{"type":"keyword"}}}`

func mappingTab(t *testing.T) (*Model, *fakeES, *esIndexTab) {
	t.Helper()
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", statusMapping, 1)
	tab := openESIndex(t, m, "orders", targetIndex)
	drain(t, m, tab.setSection(idxMapping))
	return m, f, tab
}

func TestESAddFields(t *testing.T) {
	m, f, tab := mappingTab(t)
	press(t, m, "o")
	b, err := os.ReadFile(tab.docs.editPath)
	if err != nil || !strings.Contains(string(b), `"new_field": { "type": "keyword" }`) {
		t.Fatalf("template = %s (%v)", b, err)
	}
	editorSaves(t, m, tab, `{"note":{"type":"text"}}`)
	if v := confirmText(t, m); !strings.Contains(v, "add fields to orders: note") {
		t.Fatalf("confirm:\n%s", v)
	}
	n := len(f.sent())
	press(t, m, "y")
	if b := f.lastBody("PUT orders/_mapping"); b != `{"properties":{"note":{"type":"text"}}}` {
		t.Fatalf("body = %q (sent %v)", b, f.sent())
	}
	after := f.sent()[n:]
	if i := slices.Index(after, "PUT orders/_mapping"); i < 0 || !slices.Contains(after[i:], "GET orders/_mapping") {
		t.Errorf("mapping not reread: %v", after)
	}
	if tab.status != "added 1 field" {
		t.Errorf("status = %q", tab.status)
	}
}

func TestESAddFieldsConflict(t *testing.T) {
	m, f, tab := mappingTab(t)
	press(t, m, "o")
	editorSaves(t, m, tab, `{"status":{"type":"long"}}`)
	if !strings.Contains(tab.status, "cannot change an existing field's type (status: keyword → long); reindex into a new index instead") || m.overlay != nil {
		t.Errorf("status %q overlay %T", tab.status, m.overlay)
	}
	press(t, m, "o")
	editorSaves(t, m, tab, `{"status":{"type":"keyword"}}`)
	if tab.status != "no new fields" {
		t.Errorf("status = %q", tab.status)
	}
	press(t, m, "o")
	editorSaves(t, m, tab, `{oops`)
	if !tab.statusE {
		t.Errorf("bad json: status %q", tab.status)
	}
	if slices.Contains(f.sent(), "PUT orders/_mapping") {
		t.Error("PUT sent")
	}
}

func TestESAddFieldsAlias(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", statusMapping, 1)
	f.aliases = []es.Alias{{Name: "orders-alias", Index: "orders"}}
	tab := openESIndex(t, m, "orders-alias", targetAlias)
	drain(t, m, tab.setSection(idxMapping))
	press(t, m, "o")
	if tab.status != "add fields on one index (open it from Indices)" || tab.docs.editPath != "" {
		t.Errorf("status = %q", tab.status)
	}
}

func TestESStatsShards(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("logs-1", "", 1)
	f.addIndex("logs-2", "", 1)
	tab := openESIndex(t, m, "logs-*", targetPattern)
	drain(t, m, tab.setSection(idxStats))
	press(t, m, "j", "g", "d")
	st, ok := m.activeTab().(*esServerTab)
	if !ok || st.section != secESShards || st.shardFilter != "logs-2" {
		t.Fatalf("active tab = %#v", m.activeTab())
	}
}
