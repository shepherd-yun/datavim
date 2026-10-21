package ui

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
)

func dbqFake(f *fakeES) *int {
	polls := 0
	f.routes["GET _tasks/n:1"] = func(es.Request) es.Response {
		polls++
		if polls < 2 {
			return jsonResp(map[string]any{"completed": false, "task": map[string]any{"status": map[string]int{"total": 3, "deleted": 1}}})
		}
		return jsonResp(map[string]any{"completed": true, "task": map[string]any{"status": map[string]int{"total": 3, "deleted": 3}}})
	}
	return &polls
}

func setPoll(t *testing.T) {
	old := esTaskPoll
	esTaskPoll = time.Millisecond
	t.Cleanup(func() { esTaskPoll = old })
}

func TestESDBQSingleIndex(t *testing.T) {
	setPoll(t)
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 3)
	polls := dbqFake(f)
	tab := openESIndex(t, m, "orders", targetIndex)
	tab.docs.query = "status:paid"
	press(t, m, "X")
	v := confirmText(t, m)
	if !strings.Contains(v, "delete 3 documents matching status:paid in orders") {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "wrong")
	press(t, m, "enter")
	if slices.ContainsFunc(f.sent(), func(s string) bool { return strings.Contains(s, "_delete_by_query") }) {
		t.Fatal("deleted with a wrong name")
	}
	for range 5 {
		press(t, m, "backspace")
	}
	typeText(t, m, "orders")
	press(t, m, "enter")
	if !slices.Contains(f.sent(), "POST orders/_delete_by_query?wait_for_completion=false&conflicts=proceed") {
		t.Fatalf("sent %v", f.sent())
	}
	if *polls < 2 || !strings.Contains(tab.status, "deleted 3 documents") {
		t.Errorf("polls %d status %q", *polls, tab.status)
	}
}

func TestESDBQAllOnProd(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{Env: config.EnvProd, ReadOnly: new(bool)})
	f.addIndex("orders", ordersMapping, 3)
	tab := openESIndex(t, m, "orders", targetIndex)
	press(t, m, "X")
	if m.overlay != nil || tab.status != esProdRefusal {
		t.Errorf("overlay %T status %q", m.overlay, tab.status)
	}
	if slices.ContainsFunc(f.sent(), func(s string) bool { return strings.Contains(s, "_delete_by_query") }) {
		t.Error("sent a delete")
	}
}

func TestESDBQAllNonProd(t *testing.T) {
	setPoll(t)
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 3)
	dbqFake(f)
	openESIndex(t, m, "orders", targetIndex)
	press(t, m, "X")
	if v := confirmText(t, m); !strings.Contains(v, "deletes ALL 3 documents in orders") {
		t.Fatalf("confirm:\n%s", v)
	}
}

func TestESDBQAliasExpands(t *testing.T) {
	setPoll(t)
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("a", ordersMapping, 1)
	f.addIndex("b", ordersMapping, 1)
	f.aliases = []es.Alias{{Name: "both", Index: "a"}, {Name: "both", Index: "b"}}
	dbqFake(f)
	tab := openESIndex(t, m, "both", targetAlias)
	tab.docs.query = "n:1"
	press(t, m, "X")
	v := confirmText(t, m)
	if !strings.Contains(v, "  a") || !strings.Contains(v, "  b") || !strings.Contains(v, "type both to confirm") {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "both")
	press(t, m, "enter")
	if !slices.Contains(f.sent(), "POST a,b/_delete_by_query?wait_for_completion=false&conflicts=proceed") {
		t.Errorf("sent %v", f.sent())
	}
}

func TestESDBQReadOnly(t *testing.T) {
	ro := true
	m, _, f := newESModel(t, config.Connection{ReadOnly: &ro})
	f.addIndex("orders", ordersMapping, 3)
	tab := openESIndex(t, m, "orders", targetIndex)
	press(t, m, "X")
	if m.overlay != nil || tab.status != readOnlyHint {
		t.Errorf("overlay %T status %q", m.overlay, tab.status)
	}
}

func ubqFake(f *fakeES) *int {
	polls := 0
	f.routes["POST orders/_update_by_query"] = func(es.Request) es.Response { return jsonResp(map[string]string{"task": "n:2"}) }
	f.routes["GET _tasks/n:2"] = func(es.Request) es.Response {
		polls++
		if polls < 2 {
			return jsonResp(map[string]any{"completed": false, "task": map[string]any{"status": map[string]int{"total": 3, "updated": 1}}})
		}
		return jsonResp(map[string]any{"completed": true, "task": map[string]any{"status": map[string]int{"total": 3, "updated": 3}}})
	}
	return &polls
}

func TestESUpdateByQuery(t *testing.T) {
	setPoll(t)
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 3)
	polls := ubqFake(f)
	tab := openESIndex(t, m, "orders", targetIndex)
	tab.docs.query = "status:new"
	press(t, m, "space", "u")
	b, err := os.ReadFile(tab.docs.editPath)
	if err != nil || !strings.Contains(string(b), "// update by query on orders · filter: status:new") {
		t.Fatalf("template = %s (%v)", b, err)
	}
	editorSaves(t, m, tab, string(b)+"ctx._source.status = 'old'\n")
	v := confirmText(t, m)
	if !strings.Contains(v, "update 3 documents matching status:new in orders") || !strings.Contains(v, "ctx._source.status = 'old'") ||
		!strings.Contains(v, "type orders to confirm") {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "orders")
	press(t, m, "enter")
	if !slices.Contains(f.sent(), "POST orders/_update_by_query?wait_for_completion=false") {
		t.Fatalf("sent %v", f.sent())
	}
	var body struct {
		Query  map[string]any `json:"query"`
		Script struct{ Source string }
	}
	if err := json.Unmarshal([]byte(f.lastBody("POST orders/_update_by_query")), &body); err != nil || body.Script.Source != "ctx._source.status = 'old'" || body.Query["query_string"] == nil {
		t.Errorf("body = %s", f.lastBody("POST orders/_update_by_query"))
	}
	if *polls < 2 || !strings.Contains(tab.status, "updated 3 documents") {
		t.Errorf("polls %d status %q", *polls, tab.status)
	}
}

func TestESUpdateByQueryAll(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 3)
	tab := openESIndex(t, m, "orders", targetIndex)
	press(t, m, "space", "u")
	editorSaves(t, m, tab, "ctx._source.a = 1\n")
	if v := confirmText(t, m); !strings.Contains(v, "updates ALL 3 documents in orders") || !strings.Contains(v, "type orders to confirm") {
		t.Fatalf("confirm:\n%s", v)
	}
}

func TestESUpdateByQueryEmptyScript(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 3)
	tab := openESIndex(t, m, "orders", targetIndex)
	n := len(f.sent())
	press(t, m, "space", "u")
	b, _ := os.ReadFile(tab.docs.editPath)
	editorSaves(t, m, tab, string(b))
	if tab.status != "no script; nothing to do" || m.overlay != nil || len(f.sent()) != n {
		t.Errorf("status %q overlay %T sent %v", tab.status, m.overlay, f.sent()[n:])
	}
}

func TestESUpdateByQueryReadOnly(t *testing.T) {
	ro := true
	m, _, f := newESModel(t, config.Connection{ReadOnly: &ro})
	f.addIndex("orders", ordersMapping, 3)
	tab := openESIndex(t, m, "orders", targetIndex)
	press(t, m, "space", "u")
	if tab.status != readOnlyHint || tab.docs.editPath != "" {
		t.Errorf("status %q path %q", tab.status, tab.docs.editPath)
	}
}
