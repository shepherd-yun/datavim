package ui

import (
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
