package ui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/redit"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
)

func testHash() fakeValue {
	return fakeValue{typ: "hash", hash: [][2]string{{"a", "1"}, {"b", "\xff\x00"}}}
}

// editTo presses i on the cursor's cell, checks the cell editor opened and
// replaces the text in it with v.
func editTo(t *testing.T, m *Model, v string) {
	t.Helper()
	pressAll(t, m, "i")
	if _, ok := m.overlay.(*overlay.CellEditor); !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, m.activeTab().(*keyTab).status)
	}
	pressAll(t, m, "esc", "c", "c")
	typeText(t, m, v)
	pressAll(t, m, "enter")
	if m.overlay != nil {
		t.Fatalf("the cell editor stays open: %T", m.overlay)
	}
}

func shown(kt *keyTab, row, col int) string {
	return kt.grid.Result().Rows[row][col].Text
}

func TestKeyEditStagesHash(t *testing.T) {
	m, _ := openTestKey(t, "h", testHash())
	kt := m.activeTab().(*keyTab)
	pressAll(t, m, "l")
	editTo(t, m, "9")
	if kt.changes() != 1 || kt.mark(0, 1) != grid.MarkChanged || shown(kt, 0, 1) != "9" {
		t.Fatalf("changes %d, mark %v, cell %q", kt.changes(), kt.mark(0, 1), shown(kt, 0, 1))
	}
	if !strings.Contains(screen(m), "1 unsaved change") {
		t.Fatalf("header:\n%s", screen(m))
	}
	pressAll(t, m, "u")
	if kt.changes() != 0 || shown(kt, 0, 1) != "1" {
		t.Fatalf("after u: changes %d, cell %q", kt.changes(), shown(kt, 0, 1))
	}
}

func TestKeyEditRefusesBinary(t *testing.T) {
	m, _ := openTestKey(t, "h", testHash())
	kt := m.activeTab().(*keyTab)
	pressAll(t, m, "j", "l", "i")
	if m.overlay != nil || kt.status != "binary values cannot be edited here · use the console" {
		t.Fatalf("overlay %T, status %q", m.overlay, kt.status)
	}
	if shown(kt, 1, 1) != `\xFF\x00` {
		t.Fatalf("shown escaped: %q", shown(kt, 1, 1))
	}
	pressAll(t, m, "d", "d")
	if kt.changes() != 1 || kt.mark(1, 0) != grid.MarkDeleted {
		t.Fatalf("dd: changes %d", kt.changes())
	}
}

func TestKeyEditReadOnly(t *testing.T) {
	m, b, r := newRedisModel(t, config.Connection{})
	r.put(0, "h", testHash())
	_ = b.SetReadOnly("cache", true)
	drainAll(t, m, m.openKey("cache", 0, "h"))
	kt := m.activeTab().(*keyTab)
	for _, k := range [][]string{{"i"}, {"o"}, {"d", "d"}} {
		pressAll(t, m, k...)
		if m.overlay != nil || kt.status != readOnlyHint || kt.changes() != 0 {
			t.Fatalf("%v: overlay %T, status %q", k, m.overlay, kt.status)
		}
	}
}

func TestKeyEditListColumns(t *testing.T) {
	m, _ := openTestKey(t, "l", fakeValue{typ: "list", list: []string{"a", "b"}})
	kt := m.activeTab().(*keyTab)
	pressAll(t, m, "i")
	if m.overlay != nil || kt.status != "the index column cannot be edited" {
		t.Fatalf("overlay %T, status %q", m.overlay, kt.status)
	}
	pressAll(t, m, "o")
	if kt.table.Len() != 3 || shown(kt, 1, 0) != "tail" || kt.mark(1, 0) != grid.MarkInserted {
		t.Fatalf("new row: %+v", kt.grid.Result().Rows)
	}
	pressAll(t, m, "h", "i") // a new row's index can change (head / tail)
	if _, ok := m.overlay.(*overlay.CellEditor); !ok {
		t.Fatalf("overlay %T, status %q", m.overlay, kt.status)
	}
}

func TestKeyEditZSetRank(t *testing.T) {
	m, _ := openTestKey(t, "z", fakeValue{typ: "zset", zset: []rdb.ZMember{{Member: "m", Score: 1}}})
	kt := m.activeTab().(*keyTab)
	pressAll(t, m, "i")
	if kt.status != "the rank column cannot be edited" {
		t.Fatalf("status %q", kt.status)
	}
	pressAll(t, m, "l", "l")
	editTo(t, m, "2")
	if kt.changes() != 1 {
		t.Fatalf("changes %d", kt.changes())
	}
}

func TestKeyEditStreamRows(t *testing.T) {
	m, _ := openTestKey(t, "s", fakeValue{typ: "stream", stream: []rdb.StreamEntry{{ID: "1-0", Fields: [][2]string{{"a", "1"}}}}})
	kt := m.activeTab().(*keyTab)
	pressAll(t, m, "l", "i")
	if m.overlay != nil || kt.status != "stream entries cannot be changed · delete one and add a new one" {
		t.Fatalf("overlay %T, status %q", m.overlay, kt.status)
	}
	pressAll(t, m, "o")
	if shown(kt, 1, 0) != "*" {
		t.Fatalf("new entry id = %q", shown(kt, 1, 0))
	}
}

func bigHash(n int) fakeValue {
	v := fakeValue{typ: "hash"}
	for i := range n {
		v.hash = append(v.hash, [2]string{fmt.Sprint("f", i), fmt.Sprint("v", i)})
	}
	return v
}

func TestKeyEditKeepsChangesAcrossPages(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	m.settings.PageSize = 2
	r.put(0, "h", bigHash(5))
	drainAll(t, m, m.openKey("cache", 0, "h"))
	kt := m.activeTab().(*keyTab)
	pressAll(t, m, "l")
	editTo(t, m, "new")
	pressAll(t, m, "]")
	if kt.table.Len() < 4 || kt.changes() != 1 || shown(kt, 0, 1) != "new" || kt.table.Row(0).Values[1].Text != "new" {
		t.Fatalf("len %d, changes %d, rows %+v", kt.table.Len(), kt.changes(), kt.grid.Result().Rows)
	}
}

func TestKeyPageDedup(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	m.settings.PageSize = 2
	v := bigHash(3)
	v.hash = append(v.hash[:2], v.hash[0], v.hash[2]) // HSCAN returns f0 again on page 2
	r.put(0, "h", v)
	drainAll(t, m, m.openKey("cache", 0, "h"))
	kt := m.activeTab().(*keyTab)
	pressAll(t, m, "]")
	n := 0
	for i := range kt.table.Len() {
		if kt.table.Row(i).Orig[0].Text == "f0" {
			n++
		}
	}
	if n != 1 || kt.table.Len() != 3 {
		t.Fatalf("f0 rows %d, len %d", n, kt.table.Len())
	}
}

func TestKeyEditBlocksRefresh(t *testing.T) {
	m, r := openTestKey(t, "h", testHash())
	kt := m.activeTab().(*keyTab)
	pressAll(t, m, "l")
	editTo(t, m, "9")
	before := len(r.keyCalls())
	pressAll(t, m, "space", "r")
	if len(r.keyCalls()) != before || !strings.HasPrefix(kt.status, "1 unsaved change") {
		t.Fatalf("calls %d → %d, status %q", before, len(r.keyCalls()), kt.status)
	}
	pressAll(t, m, "F")
	if m.overlay != nil {
		t.Fatalf("F with changes opened %T", m.overlay)
	}
}

func TestKeyTabCloseAsks(t *testing.T) {
	m, _ := openTestKey(t, "h", testHash())
	pressAll(t, m, "l")
	editTo(t, m, "9")
	drainAll(t, m, m.requestClose(false))
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok || len(m.tabs) != 1 {
		t.Fatalf("overlay %T, tabs %d", m.overlay, len(m.tabs))
	}
	_ = c
}

func TestKeySubmitPreview(t *testing.T) {
	for _, env := range []config.Env{config.EnvDev, config.EnvProd} {
		m, b, r := newRedisModel(t, config.Connection{Env: env})
		_ = b.SetReadOnly("cache", false)
		r.put(0, "h", testHash())
		drainAll(t, m, m.openKey("cache", 0, "h"))
		pressAll(t, m, "l")
		editTo(t, m, "9")
		pressAll(t, m, "space", "w")
		if _, ok := m.overlay.(*overlay.Preview); !ok {
			t.Fatalf("%s: overlay %T, status %q", env, m.overlay, m.activeTab().(*keyTab).status)
		}
		s := screen(m)
		title := "Submit 1 change to h"
		if env == config.EnvProd {
			title = "PROD · " + title
		}
		for _, want := range []string{title, `check: HGET h a == "1"`, "HSET h a 9"} {
			if !strings.Contains(s, want) {
				t.Fatalf("%s: preview misses %q:\n%s", env, want, s)
			}
		}
	}
}

func TestKeySubmitApplies(t *testing.T) {
	m, r := openTestKey(t, "h", testHash())
	kt := m.activeTab().(*keyTab)
	pressAll(t, m, "l")
	editTo(t, m, "9")
	before := strings.Count(strings.Join(r.keyCalls(), "\n"), "KeyInfo")
	pressAll(t, m, "space", "w", "y")
	plans := r.appliedPlans()
	if len(plans) != 1 || len(plans[0].Cmds) != 1 || strings.Join(plans[0].Cmds[0], " ") != "HSET h a 9" {
		t.Fatalf("applied %+v", plans)
	}
	if kt.status != "saved 1 change" || kt.changes() != 0 {
		t.Fatalf("status %q, changes %d", kt.status, kt.changes())
	}
	if after := strings.Count(strings.Join(r.keyCalls(), "\n"), "KeyInfo"); after != before+1 {
		t.Fatalf("KeyInfo calls %d → %d", before, after)
	}
}

func TestKeySubmitConflict(t *testing.T) {
	m, r := openTestKey(t, "h", testHash())
	r.applyErr = &rdb.ChangedError{What: `field "a" changed`}
	kt := m.activeTab().(*keyTab)
	pressAll(t, m, "l")
	editTo(t, m, "9")
	pressAll(t, m, "space", "w", "y")
	if kt.status != `key changed since loaded; refresh and retry · field "a" changed` || kt.changes() != 1 {
		t.Fatalf("status %q, changes %d", kt.status, kt.changes())
	}
}

func TestKeySubmitExecError(t *testing.T) {
	m, r := openTestKey(t, "h", testHash())
	r.applyErr = &rdb.ExecError{Index: 0, Total: 1, Err: fmt.Errorf("WRONGTYPE x")}
	kt := m.activeTab().(*keyTab)
	pressAll(t, m, "l")
	editTo(t, m, "9")
	pressAll(t, m, "space", "w", "y")
	if kt.status != "command 1 of 1 failed: WRONGTYPE x · the others were applied" || kt.changes() != 0 {
		t.Fatalf("status %q, changes %d", kt.status, kt.changes())
	}
}

func TestKeySubmitNull(t *testing.T) {
	m, _ := openTestKey(t, "h", testHash())
	kt := m.activeTab().(*keyTab)
	pressAll(t, m, "o")
	editTo(t, m, "c") // the field; the value stays NULL
	pressAll(t, m, "space", "w")
	if m.overlay != nil || kt.status != "redis has no NULL · type a value (empty is fine)" {
		t.Fatalf("overlay %T, status %q", m.overlay, kt.status)
	}
}

func TestKeySubmitTreeUpdate(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.put(0, "h", fakeValue{typ: "hash", hash: [][2]string{{"a", "1"}}})
	r.put(0, "other", fakeValue{typ: "string", str: "v"})
	db0 := expandDB0(t, m)
	if countKeys(db0) != 2 {
		t.Fatalf("keys %d", countKeys(db0))
	}
	drainAll(t, m, m.openKey("cache", 0, "h"))
	r.onApply = func(redit.Plan) { r.del(0, "h") }
	pressAll(t, m, "d", "d", "space", "w", "y")
	if countKeys(db0) != 1 || db0.Children[0].Key != "other" {
		t.Fatalf("tree:\n%s", screen(m))
	}
}

func TestTrimEditorNewline(t *testing.T) {
	for _, c := range []struct{ old, edited, want string }{
		{"abc", "abd\n", "abd"},
		{"abc\n", "abd\n", "abd\n"},
		{"abc", "abd\n\n", "abd\n"},
		{"abc", "abd", "abd"},
		{"abc", "abd\r\n", "abd"},
	} {
		if got := trimEditorNewline(c.old, c.edited); got != c.want {
			t.Errorf("trimEditorNewline(%q, %q) = %q", c.old, c.edited, got)
		}
	}
}

func TestKeyEditString(t *testing.T) {
	m, r := openTestKey(t, "s", fakeValue{typ: "string", str: "hello"})
	kt := m.activeTab().(*keyTab)
	editTo(t, m, "world")
	if kt.changes() != 1 || !strings.Contains(screen(m), "world") || !strings.Contains(screen(m), "1 unsaved change") {
		t.Fatalf("changes %d:\n%s", kt.changes(), screen(m))
	}
	pressAll(t, m, "space", "w")
	s := screen(m)
	for _, want := range []string{"Submit 1 change to s", `check: GET s == "hello"`, "SET s world KEEPTTL"} {
		if !strings.Contains(s, want) {
			t.Fatalf("preview misses %q:\n%s", want, s)
		}
	}
	pressAll(t, m, "y")
	if p := r.appliedPlans(); len(p) != 1 || p[0].RestoreTTL || kt.changes() != 0 {
		t.Fatalf("applied %+v, changes %d", p, kt.changes())
	}
	editTo(t, m, "again")
	pressAll(t, m, "u")
	if kt.changes() != 0 || strings.Contains(screen(m), "again") {
		t.Fatalf("u drops the staged value:\n%s", screen(m))
	}
}

func TestKeyEditStringRedis5(t *testing.T) {
	m, r := openTestKey(t, "s", fakeValue{typ: "string", str: "hello"})
	r.mu.Lock()
	r.version = "5.0.14"
	r.mu.Unlock()
	pressAll(t, m, "space", "r") // the version is read with the key
	editTo(t, m, "world")
	pressAll(t, m, "space", "w")
	if s := screen(m); !strings.Contains(s, "SET s world") || strings.Contains(s, "KEEPTTL") {
		t.Fatalf("preview:\n%s", s)
	}
	pressAll(t, m, "y")
	if p := r.appliedPlans(); len(p) != 1 || !p[0].RestoreTTL {
		t.Fatalf("applied %+v", p)
	}
}

// externalEdit presses <space>v, checks a temp file was written with want,
// and answers as if the editor saved text.
func externalEdit(t *testing.T, m *Model, want, text string) {
	t.Helper()
	kt := m.activeTab().(*keyTab)
	file, ok := run(kt.Handle(keymap.ExternalEdit)).(editFileMsg)
	if !ok || file.err != nil {
		t.Fatalf("msg = %#v, status %q", file, kt.status)
	}
	if data, _ := os.ReadFile(file.path); string(data) != want {
		t.Fatalf("temp file = %q", data)
	}
	_ = os.Remove(file.path)
	drainAll(t, m, func() tea.Msg { return editTextMsg{tabID: kt.id, text: text} })
}

func TestKeyEditExternalTrailingNewline(t *testing.T) {
	m, _ := openTestKey(t, "s", fakeValue{typ: "string", str: "hello"})
	kt := m.activeTab().(*keyTab)
	externalEdit(t, m, "hello", "world\n")
	if kt.staged == nil || *kt.staged != "world" {
		t.Fatalf("staged = %v", kt.staged)
	}
	externalEdit(t, m, "world", "hello\n")
	if kt.staged != nil || kt.changes() != 0 {
		t.Fatalf("the original value again is no change: %v", *kt.staged)
	}
}

func TestKeyEditLongStringUsesEditor(t *testing.T) {
	m, _ := openTestKey(t, "s", fakeValue{typ: "string", str: "a\nb"})
	kt := m.activeTab().(*keyTab)
	msg, ok := run(kt.Handle(keymap.EditCell)).(editFileMsg)
	if !ok || !strings.HasSuffix(msg.path, ".txt") {
		t.Fatalf("msg %#v", msg)
	}
	_ = os.Remove(msg.path)
	m2, _ := openTestKey(t, "j", fakeValue{typ: "string", str: `{"a":` + strings.Repeat("1", 2000) + `}`})
	msg, ok = run(m2.activeTab().(*keyTab).Handle(keymap.EditCell)).(editFileMsg)
	if !ok || !strings.HasSuffix(msg.path, ".json") {
		t.Fatalf("msg %#v", msg)
	}
	_ = os.Remove(msg.path)
}

func TestKeyEditStringRefused(t *testing.T) {
	m, _ := openTestKey(t, "big", fakeValue{typ: "string", str: strings.Repeat("x", 2<<20)})
	kt := m.activeTab().(*keyTab)
	for _, a := range []keymap.Action{keymap.EditCell, keymap.ExternalEdit} {
		if cmd := kt.Handle(a); cmd != nil || kt.status != "only the first 1 MB was loaded · edit this value in the console" {
			t.Fatalf("%s: status %q", a, kt.status)
		}
	}
	m, _ = openTestKey(t, "bin", fakeValue{typ: "string", str: "\x00\x01"})
	kt = m.activeTab().(*keyTab)
	for _, a := range []keymap.Action{keymap.EditCell, keymap.ExternalEdit} {
		if cmd := kt.Handle(a); cmd != nil || kt.status != errBinaryEdit {
			t.Fatalf("%s: status %q", a, kt.status)
		}
	}
}

func TestKeyEditExternalCell(t *testing.T) {
	m, _ := openTestKey(t, "h", testHash())
	kt := m.activeTab().(*keyTab)
	pressAll(t, m, "l")
	externalEdit(t, m, "1", "{\"a\":1}\n")
	if kt.changes() != 1 || kt.table.Row(0).Values[1].Text != `{"a":1}` {
		t.Fatalf("changes %d, row %+v", kt.changes(), kt.table.Row(0))
	}
	pressAll(t, m, "j")
	if cmd := kt.Handle(keymap.ExternalEdit); cmd != nil || kt.status != errBinaryEdit {
		t.Fatalf("binary cell: status %q", kt.status)
	}
}

func TestExpectTextQuotes(t *testing.T) {
	for v, want := range map[string]string{`{"x":1}`: `"{\"x\":1}"`, "1": `"1"`, "a\xffb": `"a\xFFb"`, `a\b`: `"a\\b"`} {
		if got := expectText(redit.Check{Expect: redit.ExpectValue, Value: v}); got != want {
			t.Errorf("expectText(%q) = %s, want %s", v, got, want)
		}
	}
}
