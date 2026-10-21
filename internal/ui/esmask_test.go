package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
)

const usersMapping = `{"properties":{"name":{"type":"keyword"},"password":{"type":"keyword"}}}`

var esMasks = []config.MaskRule{{Connections: []string{"search"}, Indices: []string{"users*"}, Fields: []string{"_source.password"}}}

// maskedUsers opens users-1 with one document, under the mask rules.
func maskedUsers(t *testing.T) (*Model, *fakeES, *esIndexTab) {
	t.Helper()
	m, _, f := newESModel(t, config.Connection{})
	m.SetMasks(esMasks)
	f.addIndex("users-1", usersMapping, 0)
	f.addDoc("users-1", "1", `{"name":"ann","password":"s3cr3t"}`)
	f.addIndex("orders", `{"properties":{"order_no":{"type":"long"},"password":{"type":"keyword"}}}`, 0)
	f.addDoc("orders", "o1", `{"order_no":1,"password":"visible"}`)
	return m, f, openESIndex(t, m, "users-1", targetIndex)
}

func colOf(t *testing.T, tab *esIndexTab, name string) int {
	t.Helper()
	i := slices.Index(tab.docs.cols, name)
	if i < 0 {
		t.Fatalf("no column %s in %v", name, tab.docs.cols)
	}
	return i
}

func TestESMaskDocs(t *testing.T) {
	m, _, tab := maskedUsers(t)
	c := colOf(t, tab, "password")
	if got := tab.docs.grid.Result().Rows[0][c].Text; got != "******" {
		t.Errorf("cell %q", got)
	}
	for _, a := range []keymap.Action{keymap.YankSource, keymap.YankRequest, keymap.YankCurl, keymap.YankRow} {
		text, _, _ := tab.yankText(a)
		if strings.Contains(text, "s3cr3t") {
			t.Errorf("%s: %s", a, text)
		}
	}
	for _, r := range tab.exportSource().jsonl.Rows {
		for _, v := range r {
			if strings.Contains(v.Text, "s3cr3t") {
				t.Errorf("export: %s", v.Text)
			}
		}
	}
	pressAll(t, m, "K")
	if s := screen(m); strings.Contains(s, "s3cr3t") || !strings.Contains(s, "******") {
		t.Errorf("detail:\n%s", s)
	}
}

func TestESMaskOtherIndex(t *testing.T) {
	m, _, _ := maskedUsers(t)
	tab := openESIndex(t, m, "orders", targetIndex)
	if got := tab.docs.grid.Result().Rows[0][colOf(t, tab, "password")].Text; got != "visible" {
		t.Errorf("cell %q", got)
	}
}

func TestESMaskEdit(t *testing.T) {
	m, f, tab := maskedUsers(t)
	tab.docs.grid.SetCursor(0, colOf(t, tab, "password"))
	pressAll(t, m, "i")
	if m.overlay != nil || tab.status != "masked fields cannot be edited" {
		t.Errorf("i: overlay %T status %q", m.overlay, tab.status)
	}
	pressAll(t, m, "space", "v")
	if tab.status != "this document has masked fields: edit single fields instead" {
		t.Errorf("<space>v: status %q", tab.status)
	}
	tab.docs.grid.SetCursor(0, colOf(t, tab, "name"))
	pressAll(t, m, "i")
	pressAll(t, m, "ctrl+u")
	typeText(t, m, "x")
	pressAll(t, m, "enter", "space", "w")
	if v := confirmText(t, m); strings.Contains(v, "******") {
		t.Errorf("confirm shows the mask:\n%s", v)
	}
	pressAll(t, m, "y")
	if !slices.Contains(f.sent(), "POST users-1/_update/1?if_seq_no=1&if_primary_term=1&refresh=wait_for") {
		t.Fatalf("sent %v", f.sent())
	}
	if b := f.lastBody("POST users-1/_update/1"); b != `{"doc":{"name":"x"}}` {
		t.Errorf("body %q", b)
	}
	if len(sentContaining(f, "PUT users-1/_doc/1")) != 0 {
		t.Errorf("sent the whole document: %v", f.sent())
	}
}

func TestESMaskOpenID(t *testing.T) {
	m, _, _ := maskedUsers(t)
	drain(t, m, func() tea.Msg { return esOpenIDMsg{conn: "search", target: "users-1", kind: targetIndex, text: "1"} })
	if s := screen(m); strings.Contains(s, "s3cr3t") || !strings.Contains(s, "******") {
		t.Errorf("screen:\n%s", s)
	}
}

func TestESMaskConsole(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	m.SetMasks(esMasks)
	f.addIndex("users-1", usersMapping, 0)
	f.addDoc("users-1", "1", `{"name":"ann","password":"s3cr3t"}`)
	c := openESConsoleWith(t, m, "POST users-1/_search\n{\"query\":{\"match_all\":{}}}\n", 0)
	press(t, m, "enter")
	if s := screen(m); strings.Contains(s, "s3cr3t") || !strings.Contains(s, "******") || !strings.Contains(s, "masked") {
		t.Fatalf("json:\n%s", s)
	}
	press(t, m, "]")
	for _, r := range c.grid.Result().Rows {
		for _, v := range r {
			if strings.Contains(v.Text, "s3cr3t") {
				t.Errorf("table: %v", r)
			}
		}
	}
	c2 := openESConsoleWith(t, m, "GET _cat/indices\n", 0)
	press(t, m, "enter")
	if c2.cur() == nil || c2.cur().masked {
		t.Errorf("_cat masked")
	}
}

func TestESMaskKafkaUnaffected(t *testing.T) {
	m, _, _ := newESModel(t, config.Connection{})
	m.SetMasks([]config.MaskRule{{Env: "dev", Fields: []string{"_source.password"}}})
	if mk := m.maskerFor("events", "orders"); mk != nil {
		t.Errorf("an ES rule masks a Kafka topic: %+v", mk)
	}
}

func TestESSimulateMasked(t *testing.T) {
	m, f, _ := maskedUsers(t)
	f.pipelines = map[string]string{"p": `{"processors":[]}`}
	pressAll(t, m, "space", "p", "enter")
	if b := f.lastBody("POST _ingest/pipeline/p/_simulate"); strings.Contains(b, "s3cr3t") {
		t.Errorf("body %s", b)
	}
	if s := screen(m); !strings.Contains(s, "masked fields sent as ******") {
		t.Errorf("screen:\n%s", s)
	}
}
