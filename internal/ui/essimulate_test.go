package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
)

func ilmExplainRoute(f *fakeES, status int, body string) {
	f.routes["GET orders-*/_ilm/explain"] = func(es.Request) es.Response { return es.Response{Status: status, Body: []byte(body)} }
}

func statsColumn(t *testing.T, tab *esIndexTab, name string) int {
	t.Helper()
	for i, c := range tab.statsGrid.Result().Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}

func TestESStatsILM(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	for _, n := range []string{"orders-1", "orders-2", "orders-3"} {
		f.addIndex(n, ordersMapping, 1)
	}
	ilmExplainRoute(f, 200, `{"indices":{
		"orders-1":{"index":"orders-1","managed":true,"policy":"p1","phase":"hot","action":"rollover","step":"check-rollover-ready"},
		"orders-2":{"index":"orders-2","managed":false},
		"orders-3":{"index":"orders-3","managed":true,"policy":"p1","phase":"warm","action":"shrink","step":"ERROR","step_info":{"reason":"too many shards"}}}}`)
	tab := openESIndex(t, m, "orders-*", targetPattern)
	press(t, m, "]", "]", "]", "]")
	if tab.section != idxStats {
		t.Fatalf("section %v", tab.section)
	}
	c := statsColumn(t, tab, "ilm")
	if c != statsColumn(t, tab, "shards")+1 {
		t.Fatalf("ilm column at %d: %+v", c, tab.statsGrid.Result().Columns)
	}
	rows := tab.statsGrid.Result().Rows
	got := []string{rows[0][c].Text, rows[1][c].Text, rows[2][c].Text}
	if !slices.Equal(got, []string{"p1 · hot/rollover", "—", "p1 · ERROR: too many shards"}) {
		t.Errorf("ilm = %q", got)
	}
	if tab.statsMark(2, 0) != grid.MarkError || tab.statsMark(0, 0) != grid.MarkNone {
		t.Error("marks")
	}

	ilmExplainRoute(f, 403, `{"error":{"type":"security_exception","reason":"no"},"status":403}`)
	pressAll(t, m, "space", "r")
	if statsColumn(t, tab, "ilm") >= 0 || tab.parts[idxStats].err != "" {
		t.Errorf("403: columns %+v err %q", tab.statsGrid.Result().Columns, tab.parts[idxStats].err)
	}
}

func TestESStatsNoILMOnOpenSearch(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.info = es.Info{Version: "2.17.0", Distribution: "opensearch", Major: 2, Minor: 17}
	f.addIndex("orders-1", ordersMapping, 1)
	tab := openESIndex(t, m, "orders-*", targetPattern)
	press(t, m, "]", "]", "]", "]")
	if statsColumn(t, tab, "ilm") >= 0 || len(sentContaining(f, "_ilm")) != 0 {
		t.Errorf("columns %+v sent %v", tab.statsGrid.Result().Columns, f.sent())
	}
}

func docsTab(t *testing.T, m *Model, f *fakeES) *esIndexTab {
	t.Helper()
	f.addIndex("orders", ordersMapping, 0)
	f.addDoc("orders", "a", `{"order_no":1}`)
	f.addDoc("orders", "b", `{"order_no":2,"status":"new"}`)
	return openESIndex(t, m, "orders", targetIndex)
}

func TestESSimulate(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.pipelines = map[string]string{
		"z-other":  `{"processors":[]}`,
		"ingest-a": `{"processors":[{"set":{"field":"x","value":1}}]}`,
	}
	tab := docsTab(t, m, f)
	f.index("orders").settings = map[string]string{"index.default_pipeline": "ingest-a"}
	f.routes["POST _ingest/pipeline/ingest-a/_simulate"] = func(es.Request) es.Response {
		return jsonResp(map[string]any{"docs": []any{map[string]any{"processor_results": []any{
			map[string]any{"processor_type": "set", "status": "error", "error": map[string]string{"type": "x", "reason": "boom"}},
		}}}})
	}
	pressAll(t, m, "j", "space", "p")
	p, ok := m.overlay.(*overlay.Picker)
	if !ok {
		t.Fatalf("overlay %T status %q", m.overlay, tab.status)
	}
	if v := screen(m); !strings.Contains(v, "Simulate pipeline on b") || strings.Index(v, "ingest-a (default)") > strings.Index(v, "z-other") {
		t.Fatalf("picker:\n%s", v)
	}
	_ = p
	pressAll(t, m, "enter")
	if !slices.Contains(f.sent(), "POST _ingest/pipeline/ingest-a/_simulate?verbose=true") {
		t.Fatalf("sent %v", f.sent())
	}
	if b := f.lastBody("POST _ingest/pipeline/ingest-a/_simulate"); !strings.Contains(b, `"_source":{"order_no":2,"status":"new"}`) || !strings.Contains(b, `"_id":"b"`) {
		t.Errorf("body %s", b)
	}
	if _, ok := m.overlay.(*overlay.Detail); !ok {
		t.Fatalf("overlay %T status %q", m.overlay, tab.status)
	}
	if v := screen(m); !strings.Contains(v, "ingest-a on b · failed at set") || !strings.Contains(v, `"processor_type": "set"`) {
		t.Errorf("detail:\n%s", v)
	}
}

func TestESSimulateNoPipelines(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	tab := docsTab(t, m, f)
	pressAll(t, m, "space", "p")
	if m.overlay != nil || tab.status != "no ingest pipelines" {
		t.Errorf("overlay %T status %q", m.overlay, tab.status)
	}
}

func TestESSimulateNoSelection(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("empty", "", 0)
	tab := openESIndex(t, m, "empty", targetIndex)
	pressAll(t, m, "space", "p")
	if m.overlay != nil || tab.status != "select a document first" {
		t.Errorf("overlay %T status %q", m.overlay, tab.status)
	}
}

func TestESSimulateReadOnly(t *testing.T) {
	ro := true
	m, _, f := newESModel(t, config.Connection{ReadOnly: &ro})
	f.pipelines = map[string]string{"p": `{"processors":[]}`}
	docsTab(t, m, f)
	pressAll(t, m, "space", "p")
	if _, ok := m.overlay.(*overlay.Picker); !ok {
		t.Fatalf("overlay %T", m.overlay)
	}
	pressAll(t, m, "enter")
	if !slices.Contains(f.sent(), "POST _ingest/pipeline/p/_simulate?verbose=true") {
		t.Errorf("sent %v", f.sent())
	}
}
