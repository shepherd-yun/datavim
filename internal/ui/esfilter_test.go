package ui

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// searchQuery is the query of the last search body sent.
func searchQuery(t *testing.T, f *fakeES) map[string]any {
	t.Helper()
	var b struct {
		Query map[string]any `json:"query"`
	}
	if err := json.Unmarshal([]byte(f.lastBody("POST _search")), &b); err != nil {
		t.Fatal(err)
	}
	return b.Query
}

func TestESFilterPrompt(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 3)
	tab := openESIndex(t, m, "orders", targetIndex)
	press(t, m, "F")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, "status:paid")
	press(t, m, "enter")
	q := searchQuery(t, f)
	if qs, ok := q["query_string"].(map[string]any); !ok || qs["query"] != "status:paid" || qs["default_operator"] != "AND" {
		t.Errorf("query = %v", q)
	}
	if tab.docs.query != "status:paid" || !strings.Contains(f.lastBody("POST _search"), `"highlight"`) {
		t.Errorf("query %q body %s", tab.docs.query, f.lastBody("POST _search"))
	}
	if !strings.Contains(screen(m), "q: status:paid") {
		t.Errorf("screen:\n%s", screen(m))
	}
	press(t, m, "F")
	for range len("status:paid") {
		press(t, m, "backspace")
	}
	typeText(t, m, `{"term":{"a":1}}`)
	press(t, m, "enter")
	if q := searchQuery(t, f); q["term"] == nil {
		t.Errorf("dsl query = %v", q)
	}
}

func TestESFilterSyntaxError(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 3)
	tab := openESIndex(t, m, "orders", targetIndex)
	f.routes["POST _search"] = func(r es.Request) es.Response {
		if strings.Contains(string(r.Body), "query_string") {
			return es.Response{Status: 400, Body: []byte(`{"error":{"root_cause":[{"type":"query_shard_exception","reason":"Failed to parse query [a:(]"}],"type":"search_phase_execution_exception","reason":"all shards failed"},"status":400}`)}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.answer(r)
	}
	press(t, m, "F")
	typeText(t, m, "a:(")
	press(t, m, "enter")
	p, ok := m.overlay.(*overlay.Prompt)
	if !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	if v := p.View(100, 20); !strings.Contains(v, "Failed to parse query [a:(]") || !strings.Contains(v, "a:(") {
		t.Errorf("prompt:\n%s", v)
	}
	if tab.docs.query != "" || docRows(tab) != 3 {
		t.Errorf("query %q rows %d", tab.docs.query, docRows(tab))
	}
	press(t, m, "{") // a JSON object that is not valid: refused before sending
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatal("prompt closed")
	}
}

func TestESFilterCellKeys(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 0)
	f.addDoc("orders", "o1", `{"user":{"name":"张三"},"status":"paid"}`)
	tab := openESIndex(t, m, "orders", targetIndex)
	col := func(name string) {
		for range len(docCols(tab)) {
			press(t, m, "h")
		}
		for range slices.Index(docCols(tab), name) {
			press(t, m, "l")
		}
	}
	col("user.name")
	press(t, m, "f", "f")
	if tab.docs.query != `user.name:"张三"` {
		t.Fatalf("ff = %q", tab.docs.query)
	}
	col("status")
	press(t, m, "f", "!")
	if tab.docs.query != `user.name:"张三" AND NOT status:"paid"` {
		t.Fatalf("f! = %q", tab.docs.query)
	}
	col("_id")
	press(t, m, "f", "f")
	if !strings.HasSuffix(tab.docs.query, ` AND _id:"o1"`) || !strings.HasPrefix(tab.docs.query, "(") {
		t.Fatalf("ff on _id = %q", tab.docs.query)
	}
	press(t, m, "f", "x")
	col("status")
	press(t, m, "f", "n")
	if tab.docs.query != "NOT _exists_:status" {
		t.Fatalf("fn = %q", tab.docs.query)
	}
	press(t, m, "f", "x")
	if tab.docs.query != "" {
		t.Fatalf("fx = %q", tab.docs.query)
	}
}

func TestESFilterComplete(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 1)
	openESIndex(t, m, "orders", targetIndex)
	press(t, m, "F")
	typeText(t, m, "status:x AND us")
	press(t, m, "tab")
	p := m.overlay.(*overlay.Prompt)
	if v := p.View(100, 20); !strings.Contains(v, "status:x AND user.name") {
		t.Errorf("completed:\n%s", v)
	}
}

func TestESFilterHistory(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 1)
	openESIndex(t, m, "orders", targetIndex)
	for _, q := range []string{"a:1", "b:2"} {
		press(t, m, "F")
		for range 3 {
			press(t, m, "backspace")
		}
		typeText(t, m, q)
		press(t, m, "enter")
	}
	press(t, m, "F", "up", "up")
	if v := m.overlay.(*overlay.Prompt).View(100, 20); !strings.Contains(v, "> a:1") {
		t.Errorf("history:\n%s", v)
	}
}

func TestESHighlight(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 0)
	f.addDoc("orders", "o1", `{"status":"paid","remark":"paid late"}`)
	f.routes["POST _search"] = func(r es.Request) es.Response {
		f.mu.Lock()
		defer f.mu.Unlock()
		resp := f.answer(r)
		if strings.Contains(string(r.Body), "highlight") {
			resp.Body = []byte(strings.Replace(string(resp.Body), `"_index":"orders"`, `"_index":"orders","highlight":{"status":["<em>paid</em>"],"remark":["<em>paid</em> late"]}`, 1))
		}
		return resp
	}
	tab := openESIndex(t, m, "orders", targetIndex)
	press(t, m, "F")
	typeText(t, m, "paid")
	press(t, m, "enter")
	if c := docCols(tab); c[len(c)-1] != "+" {
		t.Fatalf("columns = %v", c)
	}
	if v := tab.docs.grid.Result().Rows[0]; v[len(v)-1].Text != "+ remark" {
		t.Errorf("extra hits = %+v", v)
	}
	press(t, m, "j") // the cursor cell is drawn plain
	if out := m.render(); !strings.Contains(out, theme.Visual.Render("paid")) {
		t.Errorf("no highlighted hit")
	}
}
