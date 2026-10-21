package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

// openESConsoleWith opens the console of "search" with text, the cursor
// on line.
func openESConsoleWith(t *testing.T, m *Model, text string, line int) *esConsoleTab {
	t.Helper()
	drain(t, m, m.openESConsole("search"))
	c, ok := m.activeTab().(*esConsoleTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	c.editor.SetValue(text)
	c.editor.SetCursor(line, 0)
	c.onResults = false
	return c
}

func sentContaining(f *fakeES, s string) []string {
	var out []string
	for _, r := range f.sent() {
		if strings.Contains(r, s) {
			out = append(out, r)
		}
	}
	return out
}

func TestESConsoleRunAtCursor(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 1)
	f.addIndex("users", "", 1)
	c := openESConsoleWith(t, m, "GET orders/_mapping\n\n# second\nGET users/_mapping\n", 3)
	n := len(f.sent())
	press(t, m, "enter")
	if got := f.sent()[n:]; !slices.Equal(got, []string{"GET users/_mapping"}) {
		t.Fatalf("sent %v", got)
	}
	if len(c.results) != 1 || !c.onResults {
		t.Errorf("results %d", len(c.results))
	}
}

func TestESConsoleParseError(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	c := openESConsoleWith(t, m, "GET a/_search\n\nPOST b/_search\n{\"query\": \n", 0)
	n := len(f.sent())
	press(t, m, "R")
	if len(f.sent()) != n {
		t.Fatalf("sent %v", f.sent()[n:])
	}
	if line, _ := c.editor.Cursor(); line != 3 || !strings.Contains(c.status, "line 4: invalid JSON") {
		t.Errorf("cursor line %d status %q", line, c.status)
	}
}

func TestESConsoleCatFormat(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", "", 1)
	openESConsoleWith(t, m, "GET _cat/indices\nGET _cat/count?format=txt\n", 0)
	press(t, m, "R")
	if len(sentContaining(f, "_cat/indices?format=json")) != 1 || len(sentContaining(f, "_cat/count?format=txt")) != 1 {
		t.Errorf("sent %v", f.sent())
	}
}

func TestESConsoleTableView(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 0)
	f.addDoc("orders", "ord-1", `{"order_no":1}`)
	c := openESConsoleWith(t, m, "POST orders/_search\n{\"query\":{\"match_all\":{}}}\n", 0)
	press(t, m, "enter")
	if !strings.Contains(screen(m), `"_id": "ord-1"`) {
		t.Fatalf("json:\n%s", screen(m))
	}
	press(t, m, "]")
	if r := c.cur(); r == nil || !r.asTable || c.grid.Result().Columns[1].Name != "_id" {
		t.Fatalf("table: %+v", c.grid.Result())
	}
	press(t, m, "l", "g", "d")
	if _, ok := m.overlay.(*esDocDetail); !ok {
		t.Fatalf("gd: overlay %T status %q", m.overlay, c.status)
	}
	press(t, m, "esc", "[")
	if c.cur().asTable {
		t.Error("[ did not switch back")
	}
	c2 := openESConsoleWith(t, m, "GET /\n", 0)
	press(t, m, "enter", "]")
	if c2.status != "no table view for this response" {
		t.Errorf("status %q", c2.status)
	}
}

func TestESConsoleWriteConfirm(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("x", "", 0)
	openESConsoleWith(t, m, "PUT x/_doc/1\n{\"a\":1}\n", 0)
	press(t, m, "enter")
	if v := confirmText(t, m); !strings.Contains(v, "PUT x/_doc/1") {
		t.Fatalf("confirm:\n%s", v)
	}
	press(t, m, "y")
	if len(sentContaining(f, "PUT x/_doc/1")) != 1 {
		t.Fatalf("sent %v", f.sent())
	}
	openESConsoleWith(t, m, "PUT x/_doc/1\n{\"a\":1}\nGET x/_search\nPOST x/_doc\n{\"b\":2}\n", 0)
	press(t, m, "R")
	v := confirmText(t, m)
	if !strings.Contains(v, "PUT x/_doc/1") || !strings.Contains(v, "POST x/_doc") || !strings.Contains(v, "2 write requests") {
		t.Fatalf("one confirmation:\n%s", v)
	}
	press(t, m, "y")
	if m.overlay != nil || len(sentContaining(f, "POST x/_doc")) != 1 {
		t.Errorf("overlay %T sent %v", m.overlay, f.sent())
	}

	ro := true
	m2, _, f2 := newESModel(t, config.Connection{ReadOnly: &ro})
	c := openESConsoleWith(t, m2, "PUT x/_doc/1\n{}\n", 0)
	n := len(f2.sent())
	press(t, m2, "enter")
	if m2.overlay != nil || len(f2.sent()) != n || !strings.HasPrefix(c.status, readOnlyHint) {
		t.Errorf("read-only: overlay %T status %q", m2.overlay, c.status)
	}
}

func TestESConsoleDangerEach(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("a", "", 0)
	f.addIndex("b", "", 0)
	openESConsoleWith(t, m, "DELETE a\nDELETE b\n", 0)
	press(t, m, "R")
	if v := confirmText(t, m); !strings.Contains(v, "type a to confirm") {
		t.Fatalf("first:\n%s", v)
	}
	typeText(t, m, "a")
	press(t, m, "enter")
	if v := confirmText(t, m); !strings.Contains(v, "type b to confirm") {
		t.Fatalf("second:\n%s", v)
	}
	if len(sentContaining(f, "DELETE")) != 0 {
		t.Fatal("sent before every confirmation")
	}
	typeText(t, m, "b")
	press(t, m, "enter")
	if got := sentContaining(f, "DELETE"); !slices.Equal(got, []string{"DELETE a", "DELETE b"}) {
		t.Errorf("sent %v", got)
	}
}

func TestESConsoleWildcard(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{Env: config.EnvProd, ReadOnly: new(bool)})
	f.addIndex("logs-1", "", 0)
	c := openESConsoleWith(t, m, "DELETE logs-*\n", 0)
	press(t, m, "enter")
	if m.overlay != nil || !strings.HasPrefix(c.status, esProdRefusal) || len(sentContaining(f, "DELETE")) != 0 {
		t.Errorf("prod: overlay %T status %q", m.overlay, c.status)
	}
	m2, _, f2 := newESModel(t, config.Connection{})
	f2.addIndex("logs-1", "", 0)
	f2.addIndex("logs-2", "", 0)
	openESConsoleWith(t, m2, "DELETE logs-*\n", 0)
	press(t, m2, "enter")
	if v := confirmText(t, m2); !strings.Contains(v, "logs-1") || !strings.Contains(v, "type logs-* to confirm") {
		t.Fatalf("dev:\n%s", v)
	}
	f2.addIndex("logs-3", "", 0)
	typeText(t, m2, "logs-*")
	press(t, m2, "enter")
	if got := sentContaining(f2, "DELETE"); !slices.Equal(got, []string{"DELETE logs-1,logs-2"}) {
		t.Errorf("sent %v", got)
	}
}

func TestESConsoleUnknownIsDanger(t *testing.T) {
	m, _, _ := newESModel(t, config.Connection{})
	openESConsoleWith(t, m, "POST _foo\n", 0)
	press(t, m, "enter")
	if v := confirmText(t, m); !strings.Contains(v, "type _foo to confirm") {
		t.Errorf("confirm:\n%s", v)
	}
}

func TestESConsoleCurl(t *testing.T) {
	m, b, _ := newESModel(t, config.Connection{Host: "es1", User: "elastic"})
	_ = b.SetPassword("search", "hunter2", false)
	clip := captureClipboard(t)
	openESConsoleWith(t, m, "POST orders/_search\n{\"size\":1}\n", 1)
	press(t, m, "enter", "y", "u")
	if *clip != `curl -u 'elastic' -X POST 'http://es1:9200/orders/_search' -H 'Content-Type: application/json' -d '{"size":1}'` || strings.Contains(*clip, "hunter2") {
		t.Errorf("clipboard = %q", *clip)
	}
}

func TestESConsoleError(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.routes["GET x/_search"] = func(es.Request) es.Response {
		return errResp(404, "index_not_found_exception", "no such index [x]")
	}
	c := openESConsoleWith(t, m, "GET x/_search\n", 0)
	press(t, m, "enter")
	if v := ansi.Strip(m.render()); !strings.Contains(v, "index_not_found_exception: no such index [x]") {
		t.Errorf("screen:\n%s", v)
	}
	if !c.tabs.failed[0] {
		t.Error("not marked failed")
	}
	_ = overlay.Confirm{}
}

func popupLabels(c *esConsoleTab) []string {
	if c.popup == nil {
		return nil
	}
	var out []string
	for _, cand := range c.popup.Items() {
		out = append(out, cand.Label)
	}
	return out
}

func TestESConsoleComplete(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 1)
	f.addIndex("users", "", 1)
	for _, tc := range []struct {
		typed, want string
	}{
		{"GET or", "orders"},
		{"GET orders/_se", "_search"},
		{"GET _cat/ind", "indices"},
		{"GET orders/_search\n{\"query\": {\"b", "bool"},
		{"GET orders/_search\n{\"query\": {\"term\": {\"", "user.name"},
	} {
		c := openESConsoleWith(t, m, "", 0)
		c.editor.ReplaceAll("")
		press(t, m, "i")
		for _, r := range tc.typed {
			if r == '\n' {
				press(t, m, "enter")
				continue
			}
			press(t, m, string(r))
		}
		if got := popupLabels(c); !slices.Contains(got, tc.want) {
			t.Errorf("%q: popup %v, want %q", tc.typed, got, tc.want)
		}
		press(t, m, "esc", "esc")
	}
}
