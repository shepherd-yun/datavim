package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

func TestRenderValue(t *testing.T) {
	lines, used := renderValue(`{"a":1}`, fmtAuto)
	if used != fmtJSON || strings.Join(lines, "\n") != "{\n  \"a\": 1\n}" {
		t.Fatalf("json: %q %v", lines, used)
	}
	lines, used = renderValue("hello\nworld", fmtAuto)
	if used != fmtText || len(lines) != 2 {
		t.Fatalf("text: %q %v", lines, used)
	}
	lines, used = renderValue("\x00\x01ab", fmtAuto)
	if used != fmtHex || len(lines) != 1 || !strings.Contains(lines[0], "00 01 61 62") || !strings.Contains(lines[0], "..ab") {
		t.Fatalf("hex: %q %v", lines, used)
	}
	if _, used = renderValue("plain", fmtJSON); used != fmtText {
		t.Fatalf("forced json on text: %v", used)
	}
	if lines, used = renderValue(strings.Repeat("x", 17), fmtHex); used != fmtHex || len(lines) != 2 {
		t.Fatalf("hex rows: %q", lines)
	}
}

// openTestKey puts v at key in db0 and opens its tab.
func openTestKey(t *testing.T, key string, v fakeValue) (*Model, *fakeRedis) {
	t.Helper()
	m, _, r := newRedisModel(t, config.Connection{})
	r.put(0, key, v)
	drainAll(t, m, m.openKey("cache", 0, key))
	if _, ok := m.activeTab().(*keyTab); !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	return m, r
}

func TestKeyTabHash(t *testing.T) {
	var hash [][2]string
	for i := range 25 {
		hash = append(hash, [2]string{fmt.Sprint("f", i), fmt.Sprint("v", i)})
	}
	m, r := openTestKey(t, "h", fakeValue{typ: "hash", hash: hash, ttl: 3593e9})
	kt := m.activeTab().(*keyTab)
	s := screen(m)
	if !strings.Contains(s, "hash · TTL 3593s · 25 fields") || !strings.Contains(s, "listpack") {
		t.Fatalf("header:\n%s", s)
	}
	if n := kt.grid.RowCount(); n != m.settings.PageSize {
		t.Fatalf("rows = %d", n)
	}
	pressAll(t, m, "]")
	if n := kt.grid.RowCount(); n != 2*m.settings.PageSize {
		t.Fatalf("after ]: rows = %d", n)
	}
	pressAll(t, m, "F")
	typeText(t, m, "f1")
	pressAll(t, m, "enter")
	r.mu.Lock()
	last := r.match[len(r.match)-1]
	r.mu.Unlock()
	if last != "*f1*" {
		t.Fatalf("match = %q", last)
	}
}

func TestKeyTabCollections(t *testing.T) {
	cases := []struct {
		v    fakeValue
		want []string
	}{
		{fakeValue{typ: "list", list: []string{"a", "b", "c"}}, []string{"index", "value", "3 items"}},
		{fakeValue{typ: "set", set: []string{"x", "y"}}, []string{"member", "2 members"}},
		{fakeValue{typ: "zset", zset: []rdb.ZMember{{Member: "m", Score: 1.5}}}, []string{"rank", "score", "1.5", "1 items"}},
		{fakeValue{typ: "stream", stream: []rdb.StreamEntry{{ID: "1-0", Fields: [][2]string{{"a", "1"}, {"b", "2"}}}}}, []string{"1-0", "a=1 b=2"}},
		{fakeValue{typ: "ReJSON-RL"}, []string{"ReJSON-RL keys can be read in the console"}},
	}
	for _, c := range cases {
		m, _ := openTestKey(t, "k", c.v)
		s := screen(m)
		for _, w := range c.want {
			if !strings.Contains(s, w) {
				t.Errorf("%s: missing %q:\n%s", c.v.typ, w, s)
			}
		}
	}
}

func TestKeyTabBigString(t *testing.T) {
	m, r := openTestKey(t, "big", fakeValue{typ: "string", str: strings.Repeat("x", 2<<20)})
	r.mu.Lock()
	got := slices.Clone(r.getMax)
	r.mu.Unlock()
	if len(got) != 1 || got[0] != 1<<20 {
		t.Fatalf("GetString max = %v", got)
	}
	if s := screen(m); !strings.Contains(s, "showing first 1 MB of 2097152") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestKeyTabFormatCycle(t *testing.T) {
	m, _ := openTestKey(t, "j", fakeValue{typ: "string", str: `{"a":1}`})
	header := func() string { return strings.Split(m.activeTab().View(true), "\n")[0] }
	for i, want := range []string{"json", "text", "json", "hex", "auto"} {
		if i > 0 {
			pressAll(t, m, "v")
		}
		if h := header(); !strings.Contains(h, want) {
			t.Fatalf("step %d: header %q, want %q", i, h, want)
		}
	}
}

func TestKeyTabVanished(t *testing.T) {
	m, r := openTestKey(t, "l", fakeValue{typ: "list", list: []string{"a", "b", "c", "d", "e"}})
	r.del(0, "l")
	pressAll(t, m, "space", "r")
	if s := screen(m); !strings.Contains(s, "key no longer exists") {
		t.Fatalf("screen:\n%s", s)
	}
	before := len(r.keyCalls())
	pressAll(t, m, "]")
	if after := len(r.keyCalls()); after != before {
		t.Fatalf("] on a gone key made %d calls", after-before)
	}
}

func TestKeyTabReuse(t *testing.T) {
	m, _ := openTestKey(t, "s", fakeValue{typ: "string", str: "v"})
	drainAll(t, m, m.openKey("cache", 0, "s"))
	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d", len(m.tabs))
	}
}

func TestKeyTabBinaryTitle(t *testing.T) {
	m, r := openTestKey(t, "\xff", fakeValue{typ: "string", str: "v"})
	if title := m.activeTab().Title(); title != `\xFF` {
		t.Fatalf("title = %q", title)
	}
	if calls := r.keyCalls(); calls[0] != "KeyInfo \xff" {
		t.Fatalf("calls = %q", calls)
	}
}

func TestKeyTabFromTree(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.put(0, "solo", fakeValue{typ: "string", str: "hello"})
	expandDB0(t, m)
	pressAll(t, m, "j", "enter")
	kt, ok := m.activeTab().(*keyTab)
	if !ok || kt.key != "solo" || !strings.Contains(screen(m), "hello") {
		t.Fatalf("tab = %T\n%s", m.activeTab(), screen(m))
	}
	pressAll(t, m, "space", "k") // the key tab gives the db
	if m.overlay == nil {
		t.Fatalf("no prompt: %q", m.status)
	}
}

func TestKeyTabCopiesStringValue(t *testing.T) {
	clip := captureClipboard(t)
	m, _ := openTestKey(t, "s", fakeValue{typ: "string", str: "a\nb"})
	pressAll(t, m, "y", "l")
	if *clip != "a\nb" {
		t.Fatalf("clipboard = %q", *clip)
	}
}

func TestYankRedisKeyName(t *testing.T) {
	clip := captureClipboard(t)
	m, _, r := newRedisModel(t, config.Connection{})
	r.put(0, "a b", fakeValue{typ: "string", str: "v"})
	expandDB0(t, m)
	pressAll(t, m, "j")
	if n := m.tree.Selected(); n.Kind != tree.KindRedisKey {
		t.Fatalf("selected %+v", n)
	}
	pressAll(t, m, "y", "y")
	if *clip != `"a b"` {
		t.Fatalf("clipboard = %q", *clip)
	}
}

func TestKeyTabPassword(t *testing.T) {
	m, b, r := newRedisModel(t, config.Connection{})
	r.put(0, "s", fakeValue{typ: "string", str: "v"})
	b.needPassword = true
	drainAll(t, m, m.openKey("cache", 0, "s"))
	if m.overlay == nil {
		t.Fatal("no password prompt")
	}
	typeText(t, m, "pw")
	pressAll(t, m, "enter")
	if kt := m.activeTab().(*keyTab); !kt.info.Exists {
		t.Fatalf("not reloaded: %+v", kt.info)
	}
}
