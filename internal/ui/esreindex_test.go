package ui

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

func reindexModel(t *testing.T, cfg config.Connection) (*Model, *fakeES) {
	t.Helper()
	m, _, f := newESModel(t, cfg)
	ix := f.addIndex("orders", statusMapping, 2)
	ix.settings = map[string]string{"index.uuid": "abc", "index.analysis.analyzer.my.type": "custom", "index.creation_date": "1"}
	return m, f
}

// startReindex opens the form on orders and submits dest.
func startReindex(t *testing.T, m *Model, dest string) {
	t.Helper()
	drain(t, m, func() tea.Msg { return esReindexMsg{conn: "search", source: "orders"} })
	if _, ok := m.overlay.(*overlay.ReindexForm); !ok {
		t.Fatalf("overlay = %T (status %q)", m.overlay, m.status)
	}
	for range 20 {
		press(t, m, "backspace")
	}
	typeText(t, m, dest)
	press(t, m, "ctrl+s")
}

func writes(f *fakeES) []string {
	var out []string
	for _, s := range f.sent() {
		if !strings.HasPrefix(s, "GET ") && !strings.HasPrefix(s, "HEAD ") {
			out = append(out, s)
		}
	}
	return out
}

func TestESReindexCreateDest(t *testing.T) {
	m, f := reindexModel(t, config.Connection{})
	startReindex(t, m, "orders-v2")
	if _, ok := m.overlay.(*overlay.Choice); !ok {
		t.Fatalf("overlay = %T (status %q)", m.overlay, m.status)
	}
	press(t, m, "enter") // create it with orders' mapping and settings
	if v := confirmText(t, m); !strings.Contains(v, "reindex orders → orders-v2 · op_type index · conflicts abort") ||
		!strings.Contains(v, "create orders-v2: 1 shard, 1 replica, orders's mapping") {
		t.Fatalf("confirm:\n%s", v)
	}
	press(t, m, "y")
	if got := writes(f); !slices.Equal(got, []string{"PUT orders-v2", "POST _reindex?wait_for_completion=false"}) {
		t.Fatalf("writes = %v", got)
	}
	var body struct {
		Settings map[string]any `json:"settings"`
		Mappings map[string]any `json:"mappings"`
	}
	if err := json.Unmarshal([]byte(f.lastBody("PUT orders-v2")), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body.Settings["index.uuid"]; ok || body.Settings["index.analysis.analyzer.my.type"] != "custom" || body.Mappings["properties"] == nil {
		t.Errorf("create body = %s", f.lastBody("PUT orders-v2"))
	}
	if tab, ok := m.activeTab().(*esServerTab); !ok || tab.section != secESTasks {
		t.Errorf("active tab = %T", m.activeTab())
	}
	if !strings.Contains(m.status, "reindex started: n:3") {
		t.Errorf("status = %q", m.status)
	}
}

func TestESReindexDynamicDest(t *testing.T) {
	m, f := reindexModel(t, config.Connection{})
	startReindex(t, m, "orders-v2")
	press(t, m, "j", "enter", "y")
	if got := writes(f); !slices.Equal(got, []string{"POST _reindex?wait_for_completion=false"}) {
		t.Fatalf("writes = %v", got)
	}
}

func TestESReindexExistingDest(t *testing.T) {
	m, f := reindexModel(t, config.Connection{})
	f.addIndex("orders-v2", "", 0)
	startReindex(t, m, "orders-v2")
	if v := confirmText(t, m); !strings.Contains(v, "reindex orders → orders-v2") {
		t.Fatalf("confirm:\n%s", v)
	}
	press(t, m, "y")
	if got := writes(f); !slices.Equal(got, []string{"POST _reindex?wait_for_completion=false"}) {
		t.Fatalf("writes = %v", got)
	}
	if b := f.lastBody("POST _reindex"); b != `{"source":{"index":"orders"},"dest":{"index":"orders-v2","op_type":"index"},"conflicts":"abort"}` {
		t.Errorf("body = %s", b)
	}
}

func TestESReindexAliasDest(t *testing.T) {
	m, f := reindexModel(t, config.Connection{})
	f.addIndex("orders-2", "", 0)
	f.aliases = []es.Alias{{Name: "orders-all", Index: "orders-2"}}
	startReindex(t, m, "orders-all")
	if m.overlay != nil || m.status != "orders-all is an alias; reindex into an index" {
		t.Errorf("overlay %T status %q", m.overlay, m.status)
	}
	if got := writes(f); len(got) != 0 {
		t.Errorf("writes = %v", got)
	}
}

func TestESReindexCreateFails(t *testing.T) {
	m, f := reindexModel(t, config.Connection{})
	f.routes["PUT orders-v2"] = func(es.Request) es.Response {
		return errResp(400, "illegal_argument_exception", "bad settings")
	}
	startReindex(t, m, "orders-v2")
	press(t, m, "enter", "y")
	if !strings.Contains(m.status, "bad settings") || !m.statusErr {
		t.Errorf("status %q", m.status)
	}
	if slices.Contains(f.sent(), "POST _reindex?wait_for_completion=false") {
		t.Error("reindexed after a failed create")
	}
}

func TestESReindexReadOnly(t *testing.T) {
	ro := true
	m, _ := reindexModel(t, config.Connection{ReadOnly: &ro})
	drain(t, m, func() tea.Msg { return esReindexMsg{conn: "search", source: "orders"} })
	if m.overlay != nil || m.status != readOnlyHint {
		t.Errorf("overlay %T status %q", m.overlay, m.status)
	}
}
