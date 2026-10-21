package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/rdb"
	"github.com/shepherd-yun/datavim/internal/ui/uitest"
)

func biggestKeys(ts *typeStats) []string {
	var out []string
	for _, k := range ts.biggest {
		out = append(out, fmt.Sprintf("%s(%d)", k.Key, k.Len))
	}
	return out
}

func TestBigKeyStats(t *testing.T) {
	s := newBigKeyStats(2)
	s.add([]rdb.KeySize{
		{Key: "a", Type: "hash", Len: 3, Mem: -1},
		{Key: "b", Type: "hash", Len: 10, Mem: -1},
		{Key: "c", Type: "hash", Len: 5, Mem: -1},
		{Key: "s", Type: "string", Len: 100, Mem: -1},
	})
	h := s.byType["hash"]
	if got := biggestKeys(h); !slices.Equal(got, []string{"b(10)", "c(5)"}) || h.count != 3 || h.total != 18 {
		t.Fatalf("hash = %v count %d total %d", got, h.count, h.total)
	}
	keys, rs := s.rows()
	var order []string
	for _, k := range keys {
		order = append(order, k.Key)
	}
	if !slices.Equal(order, []string{"s", "b", "c"}) || len(rs.Rows) != 3 {
		t.Fatalf("rows = %v", order)
	}
	if rs.Rows[0][2].Text != "100 B" || rs.Rows[1][2].Text != "10 fields" || rs.Rows[1][3].Text != "" {
		t.Fatalf("cells = %v", rs.Rows)
	}
	sum := s.summary()
	if len(sum) != 2 || sum[0] != "string  1 key · 100 B · biggest s (100 B)" || sum[1] != "hash  3 keys · 18 fields · biggest b (10 fields)" {
		t.Fatalf("summary = %q", sum)
	}
}

func TestBigKeyStatsDedup(t *testing.T) {
	s := newBigKeyStats(5)
	s.add([]rdb.KeySize{{Key: "b", Type: "hash", Len: 10}, {Key: "a", Type: "hash", Len: 3}})
	s.add([]rdb.KeySize{{Key: "b", Type: "hash", Len: 10}})
	if got := biggestKeys(s.byType["hash"]); !slices.Equal(got, []string{"b(10)", "a(3)"}) {
		t.Fatalf("biggest = %v", got)
	}
	s.add([]rdb.KeySize{{Key: "b", Type: "hash", Len: 12}})
	if got := biggestKeys(s.byType["hash"]); !slices.Equal(got, []string{"b(12)", "a(3)"}) {
		t.Fatalf("after growing = %v", got)
	}
}

func TestBigKeyStatsModule(t *testing.T) {
	s := newBigKeyStats(5)
	s.add([]rdb.KeySize{{Key: "j", Type: "ReJSON-RL", Len: -1, Mem: -1}, {Key: "z", Type: "zset", Len: 2, Mem: 64}})
	if ts := s.byType["ReJSON-RL"]; ts == nil || ts.count != 1 {
		t.Fatalf("module = %+v", ts)
	}
	keys, rs := s.rows()
	if len(keys) != 1 || keys[0].Key != "z" || rs.Rows[0][2].Text != "2 members" || rs.Rows[0][3].Text != "64 B" {
		t.Fatalf("rows = %v %v", keys, rs.Rows)
	}
	if sum := s.summary(); len(sum) != 2 || sum[1] != "ReJSON-RL  1 key" {
		t.Fatalf("summary = %q", sum)
	}
}

// bigKeysModel is db0 of "cache" with 2,500 strings and the hash big
// (1,000 fields), db0 selected in the tree.
func bigKeysModel(t *testing.T, cfg config.Connection) (*Model, *fakeRedis) {
	t.Helper()
	m, _, r := newRedisModel(t, cfg)
	r.fill(0, "s", 2500)
	r.put(0, "big", bigHash(1000))
	expandDB0(t, m)
	return m, r
}

// drainBig is drainAll that feeds in only the first n big-key batches and
// returns the later ones unprocessed.
func drainBig(t *testing.T, m *Model, cmd tea.Cmd, n int) []tea.Msg {
	t.Helper()
	var held []tea.Msg
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case bigKeysStepMsg:
			if n == 0 {
				held = append(held, msg)
				continue
			}
			n--
			_, next := m.Update(msg)
			queue = append(queue, next)
		default:
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
	return held
}

func pressBig(t *testing.T, m *Model, n int, keys ...string) []tea.Msg {
	t.Helper()
	var held []tea.Msg
	for _, k := range keys {
		_, cmd := m.Update(uitest.Key(k))
		held = append(held, drainBig(t, m, cmd, n)...)
	}
	return held
}

func bigKeysHeader(m *Model) string {
	return strings.Split(m.activeTab().View(true), "\n")[0]
}

func TestBigKeysRun(t *testing.T) {
	m, r := bigKeysModel(t, config.Connection{})
	before := len(r.scanCalls()) // the tree's own
	pressAll(t, m, "B")
	if s := screen(m); !strings.Contains(s, "Analyze big keys in cache/db0 (*)") || !strings.Contains(s, "Lengths and memory") {
		t.Fatalf("choice:\n%s", s)
	}
	pressAll(t, m, "1")
	bt, ok := m.activeTab().(*bigKeysTab)
	if !ok {
		t.Fatalf("active %T", m.activeTab())
	}
	if bt.Title() != "cache/db0 · big keys" {
		t.Fatalf("title %q", bt.Title())
	}
	if h := bigKeysHeader(m); !strings.HasPrefix(h, "done · scanned 2,501 keys") {
		t.Fatalf("header %q", h)
	}
	keys, rs := bt.stats.rows()
	found := false
	for i, k := range keys {
		if k.Key == "big" && rs.Rows[i][2].Text == "1,000 fields" && rs.Rows[i][3].Text == "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no big row: %v", rs.Rows)
	}
	calls := r.scanCalls()[before:]
	if len(calls) != 3 {
		t.Fatalf("scans = %d", len(calls))
	}
	for _, c := range calls {
		if c.count != 1000 || c.match != "*" {
			t.Fatalf("scan %+v", c)
		}
	}
	if s := screen(m); !strings.Contains(s, "hash  1 key · 1,000 fields · biggest big (1,000 fields)") {
		t.Fatalf("summary:\n%s", s)
	}
}

func TestBigKeysMemory(t *testing.T) {
	m, _ := bigKeysModel(t, config.Connection{})
	pressAll(t, m, "B", "2")
	_, rs := m.activeTab().(*bigKeysTab).stats.rows()
	if rs.Rows[len(rs.Rows)-1][3].Text == "" {
		t.Fatalf("no memory: %v", rs.Rows[len(rs.Rows)-1])
	}
}

func TestBigKeysProdConfirm(t *testing.T) {
	m, r := bigKeysModel(t, config.Connection{Env: config.EnvProd})
	tree := len(r.scanCalls())
	pressAll(t, m, "B", "1")
	if s := screen(m); !strings.Contains(s, "Scan every key matching * in PROD cache/db0?") {
		t.Fatalf("confirm:\n%s", s)
	}
	pressAll(t, m, "esc")
	if m.activeTab() != nil || len(r.scanCalls()) != tree {
		t.Fatalf("esc: tab %T, scans %d", m.activeTab(), len(r.scanCalls())-tree)
	}
	pressAll(t, m, "B", "1", "y")
	if _, ok := m.activeTab().(*bigKeysTab); !ok || len(r.scanCalls()) != tree+3 {
		t.Fatalf("y: tab %T, scans %d", m.activeTab(), len(r.scanCalls()))
	}
}

func TestBigKeysCloseStops(t *testing.T) {
	m, r := bigKeysModel(t, config.Connection{})
	held := pressBig(t, m, 1, "B", "1")
	if len(held) != 1 {
		t.Fatalf("held %d", len(held))
	}
	// another tab does not stop it
	drainBig(t, m, func() tea.Msg { return openKeyMsg{conn: "cache", db: 0, key: "big"} }, 0)
	if _, ok := m.activeTab().(*keyTab); !ok {
		t.Fatalf("active %T", m.activeTab())
	}
	before := len(r.scanCalls())
	held = drainBig(t, m, func() tea.Msg { return held[0] }, 1)
	if len(r.scanCalls()) != before+1 || len(held) != 1 {
		t.Fatalf("hidden: scans %d -> %d, held %d", before, len(r.scanCalls()), len(held))
	}
	// closing it does
	pressBig(t, m, 0, "g", "T")
	bt := m.activeTab().(*bigKeysTab)
	m.Update(closeTabConfirmedMsg{tabID: bt.ID()})
	if bt.ctx.Err() == nil {
		t.Fatal("not cancelled")
	}
	before = len(r.scanCalls())
	drainBig(t, m, func() tea.Msg { return held[0] }, 1)
	if len(r.scanCalls()) != before {
		t.Fatalf("closed: scans %d -> %d", before, len(r.scanCalls()))
	}
}

func TestBigKeysCancel(t *testing.T) {
	m, r := bigKeysModel(t, config.Connection{})
	held := pressBig(t, m, 1, "B", "1")
	pressBig(t, m, 0, "ctrl+c")
	before := len(r.scanCalls())
	drainBig(t, m, func() tea.Msg { return held[0] }, 1)
	if len(r.scanCalls()) != before {
		t.Fatalf("scans %d -> %d", before, len(r.scanCalls()))
	}
	if h := bigKeysHeader(m); !strings.HasPrefix(h, "stopped · scanned 1,000 of 2,501 keys") {
		t.Fatalf("header %q", h)
	}
	if bt := m.activeTab().(*bigKeysTab); bt.grid.RowCount() == 0 {
		t.Fatal("results dropped")
	}
	// refresh starts it again and runs it to the end
	pressAll(t, m, "space", "r")
	if h := bigKeysHeader(m); !strings.HasPrefix(h, "done · scanned 2,501 keys") {
		t.Fatalf("after refresh %q", h)
	}
}

func TestBigKeysReopenWhileRunning(t *testing.T) {
	m, r := bigKeysModel(t, config.Connection{})
	pressBig(t, m, 1, "B", "1")
	before := len(r.scanCalls())
	m.setFocus(focusTree)
	pressBig(t, m, 0, "B", "1")
	if _, ok := m.activeTab().(*bigKeysTab); !ok || len(m.tabs) != 1 || len(r.scanCalls()) != before {
		t.Fatalf("tabs %d, scans %d -> %d", len(m.tabs), before, len(r.scanCalls()))
	}
}

func TestBigKeysOpenKey(t *testing.T) {
	m, _ := bigKeysModel(t, config.Connection{})
	pressAll(t, m, "B", "1")
	bt := m.activeTab().(*bigKeysTab)
	keys, _ := bt.stats.rows()
	i := slices.IndexFunc(keys, func(k rdb.KeySize) bool { return k.Key == "big" })
	for range i {
		pressAll(t, m, "j")
	}
	pressAll(t, m, "enter")
	if kt, ok := m.activeTab().(*keyTab); !ok || kt.key != "big" {
		t.Fatalf("active %T", m.activeTab())
	}
}

func TestBigKeysFolderPattern(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.put(0, "user:1", fakeValue{typ: "string", str: "v"})
	r.put(0, "user:2", fakeValue{typ: "string", str: "v"})
	r.put(0, "solo", fakeValue{typ: "string", str: "v"})
	expandDB0(t, m)
	pressAll(t, m, "j")
	before := len(r.scanCalls())
	pressAll(t, m, "B")
	if s := screen(m); !strings.Contains(s, "(user:*)") {
		t.Fatalf("choice:\n%s", s)
	}
	pressAll(t, m, "1")
	calls := r.scanCalls()[before:]
	if len(calls) == 0 || calls[0].match != "user:*" {
		t.Fatalf("scans %+v", calls)
	}
}

func TestBigKeysNotRedis(t *testing.T) {
	m, _ := newTestModel(t)
	m.setFocus(focusTree)
	press(t, m, "B")
	if m.status != "select a Redis db to analyze" {
		t.Fatalf("status %q", m.status)
	}
}
