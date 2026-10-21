package ui

import (
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

const amountMapping = `{"properties":{"amount":{"type":"long"},"status":{"type":"keyword"},"a":{"properties":{"b":{"type":"long"}}}}}`

// toColumn moves the Documents cursor to col.
func toColumn(t *testing.T, m *Model, tab *esIndexTab, col string) {
	t.Helper()
	at := slices.Index(docCols(tab), col)
	if at < 0 {
		t.Fatalf("no column %s in %v", col, docCols(tab))
	}
	for range len(docCols(tab)) {
		press(t, m, "h")
	}
	for range at {
		press(t, m, "l")
	}
}

// editCell types text into the cell under the cursor.
func editCell(t *testing.T, m *Model, text string) {
	t.Helper()
	press(t, m, "i")
	if _, ok := m.overlay.(*overlay.CellEditor); !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, m.status)
	}
	for range 20 {
		press(t, m, "backspace")
	}
	typeText(t, m, text)
	press(t, m, "enter")
}

func confirmText(t *testing.T, m *Model) string {
	t.Helper()
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	return ansi.Strip(c.View(120, 40))
}

func newEditModel(t *testing.T, cfg config.Connection) (*Model, *fakeES, *esIndexTab) {
	t.Helper()
	m, _, f := newESModel(t, cfg)
	f.addIndex("orders", amountMapping, 0)
	d := f.addDoc("orders", "ord-1", `{"amount":99,"status":"paid"}`)
	d.seq, d.term = 1022, 3
	tab := openESIndex(t, m, "orders", targetIndex)
	return m, f, tab
}

func TestESEditCell(t *testing.T) {
	m, f, tab := newEditModel(t, config.Connection{})
	toColumn(t, m, tab, "amount")
	editCell(t, m, "12.5")
	if got := tab.docs.grid.Result().Rows[0][slices.Index(docCols(tab), "amount")].Text; got != "12.5" {
		t.Fatalf("shown = %q", got)
	}
	press(t, m, "space", "w")
	if v := confirmText(t, m); !strings.Contains(v, "amount: 99 → 12.5") || !strings.Contains(v, "1 request") {
		t.Fatalf("confirm:\n%s", v)
	}
	press(t, m, "y")
	if !slices.Contains(f.sent(), "PUT orders/_doc/ord-1?if_seq_no=1022&if_primary_term=3&refresh=wait_for") {
		t.Fatalf("sent %v", f.sent())
	}
	if b := f.lastBody("PUT orders/_doc/ord-1"); b != `{"amount":12.5,"status":"paid"}` {
		t.Errorf("body = %s", b)
	}
	if len(tab.docs.changes) != 0 || !strings.Contains(tab.status, "saved 1 document") {
		t.Errorf("changes %d status %q", len(tab.docs.changes), tab.status)
	}
}

func TestESEditBadType(t *testing.T) {
	m, _, tab := newEditModel(t, config.Connection{})
	toColumn(t, m, tab, "amount")
	editCell(t, m, "x")
	if len(tab.docs.changes) != 0 || !strings.Contains(tab.status, "want a number") {
		t.Errorf("changes %d status %q", len(tab.docs.changes), tab.status)
	}
}

func TestESEditConflict(t *testing.T) {
	m, f, tab := newEditModel(t, config.Connection{})
	f.routes["PUT orders/_doc/ord-1"] = func(es.Request) es.Response {
		return errResp(409, "version_conflict_engine_exception", "[ord-1]: version conflict")
	}
	toColumn(t, m, tab, "status")
	editCell(t, m, "new")
	press(t, m, "space", "w", "y")
	if !strings.Contains(tab.status, "ord-1 changed since you loaded it; reload to see it") || len(tab.docs.changes) != 1 {
		t.Errorf("status %q changes %d", tab.status, len(tab.docs.changes))
	}
}

func TestESEditAliasWritesRealIndex(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders-1", `{"properties":{"code":{"type":"long"}}}`, 0)
	f.addIndex("orders-2", `{"properties":{"code":{"type":"keyword"}}}`, 0)
	f.aliases = []es.Alias{{Name: "orders", Index: "orders-1"}, {Name: "orders", Index: "orders-2"}}
	f.addDoc("orders-2", "x", `{"code":"7"}`)
	tab := openESIndex(t, m, "orders", targetAlias)
	toColumn(t, m, tab, "code")
	editCell(t, m, "abc") // orders-2 maps code as keyword: a string
	if len(tab.docs.changes) != 1 {
		t.Fatalf("status %q", tab.status)
	}
	press(t, m, "space", "w", "y")
	if !slices.ContainsFunc(f.sent(), func(s string) bool { return strings.HasPrefix(s, "PUT orders-2/_doc/x?") }) {
		t.Errorf("sent %v", f.sent())
	}
	if b := f.lastBody("PUT orders-2/_doc/x"); b != `{"code":"abc"}` {
		t.Errorf("body = %s", b)
	}
}

func TestESEditDottedKey(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", amountMapping, 0)
	f.addDoc("orders", "d", `{"a.b":1}`)
	tab := openESIndex(t, m, "orders", targetIndex)
	toColumn(t, m, tab, "a.b")
	editCell(t, m, "2")
	press(t, m, "space", "w", "y")
	if b := f.lastBody("PUT orders/_doc/d"); b != `{"a.b":2}` {
		t.Errorf("body = %s", b)
	}
}

func TestESEditRefused(t *testing.T) {
	m, _, tab := newEditModel(t, config.Connection{})
	tab.docs.hits[0].Truncated = true
	press(t, m, "i")
	if m.overlay != nil || !strings.Contains(tab.status, "over 1 MB") {
		t.Errorf("truncated: overlay %T status %q", m.overlay, tab.status)
	}
	m2, _, f2 := newESModel(t, config.Connection{})
	f2.addIndex("logs", `{"_source":{"enabled":false},"properties":{"a":{"type":"long"}}}`, 1)
	tab2 := openESIndex(t, m2, "logs", targetIndex)
	press(t, m2, "i")
	if m2.overlay != nil || !strings.Contains(tab2.status, "_source") {
		t.Errorf("no _source: overlay %T status %q", m2.overlay, tab2.status)
	}
}

func TestESEditReadOnly(t *testing.T) {
	ro := true
	m, f, tab := newEditModel(t, config.Connection{ReadOnly: &ro})
	toColumn(t, m, tab, "status")
	editCell(t, m, "x")
	n := len(f.sent())
	press(t, m, "space", "w")
	if m.overlay != nil || tab.status != readOnlyHint || len(f.sent()) != n {
		t.Errorf("overlay %T status %q", m.overlay, tab.status)
	}
}

func TestESEditProdShowsRequest(t *testing.T) {
	m, _, tab := newEditModel(t, config.Connection{Env: config.EnvProd, ReadOnly: new(bool)})
	toColumn(t, m, tab, "status")
	editCell(t, m, "x")
	press(t, m, "space", "w")
	if v := confirmText(t, m); !strings.Contains(v, "PUT orders/_doc/ord-1") || !strings.Contains(v, `"status": "x"`) {
		t.Errorf("confirm:\n%s", v)
	}
}

func TestESEditUndo(t *testing.T) {
	m, _, tab := newEditModel(t, config.Connection{})
	toColumn(t, m, tab, "status")
	editCell(t, m, "a")
	editCell(t, m, "b")
	press(t, m, "u")
	if c := tab.docs.changes[docKey("orders", "ord-1")]; c == nil || !strings.Contains(string(c.src), `"a"`) {
		t.Fatalf("after u: %+v", tab.docs.changes)
	}
	press(t, m, "U")
	if len(tab.docs.changes) != 0 {
		t.Errorf("after U: %+v", tab.docs.changes)
	}
}

// editorSaves answers the $EDITOR the tab opened with text.
func editorSaves(t *testing.T, m *Model, tab *esIndexTab, text string) {
	t.Helper()
	path := tab.docs.editPath
	if path == "" {
		t.Fatalf("no temp file; status %q", tab.status)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	id := tab.id
	drain(t, m, func() tea.Msg { return editDoneMsg{tabID: id, path: path} })
}

func TestESNewAndDelete(t *testing.T) {
	m, f, tab := newEditModel(t, config.Connection{})
	press(t, m, "o")
	b, _ := os.ReadFile(tab.docs.editPath)
	if !strings.Contains(string(b), `"_id": ""`) || !strings.Contains(string(b), `"amount": null`) {
		t.Errorf("template = %s", b)
	}
	editorSaves(t, m, tab, `{"_id": "", "a": 1}`)
	press(t, m, "d", "d")
	press(t, m, "space", "w")
	v := confirmText(t, m)
	if !strings.Contains(v, "new document in orders") || !strings.Contains(v, "delete orders/ord-1") || !strings.Contains(v, "2 requests") {
		t.Fatalf("confirm:\n%s", v)
	}
	press(t, m, "y")
	sent := f.sent()
	if !slices.Contains(sent, "POST orders/_doc?refresh=wait_for") || !slices.Contains(sent, "DELETE orders/_doc/ord-1?if_seq_no=1022&if_primary_term=3&refresh=wait_for") {
		t.Errorf("sent %v", sent)
	}
	if b := f.lastBody("POST orders/_doc"); b != `{"a":1}` {
		t.Errorf("create body = %s", b)
	}
}

func TestESEditWholeSource(t *testing.T) {
	m, f, tab := newEditModel(t, config.Connection{})
	press(t, m, "space", "v")
	editorSaves(t, m, tab, `{"amount": 1}`)
	press(t, m, "space", "w", "y")
	if b := f.lastBody("PUT orders/_doc/ord-1"); b != `{"amount":1}` {
		t.Errorf("body = %s", b)
	}
	press(t, m, "space", "v")
	editorSaves(t, m, tab, `[1]`)
	if !strings.Contains(tab.status, "JSON object") {
		t.Errorf("status %q", tab.status)
	}
}
