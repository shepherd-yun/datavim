package ui

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func TestFormatFact(t *testing.T) {
	cases := []struct {
		f    db.Fact
		want string
	}{
		{db.Fact{Kind: db.FactCount, Num: 87, Of: 500}, "87 / 500"},
		{db.Fact{Kind: db.FactCount, Num: 1234}, "1,234"},
		{db.Fact{Kind: db.FactBytes, Num: 12 << 30}, humanBytes(12 << 30)},
		{db.Fact{Kind: db.FactBytes, Num: 1 << 20, Of: 4 << 20}, humanBytes(1<<20) + " / " + humanBytes(4<<20)},
		{db.Fact{Kind: db.FactPercent, Num: 99.84}, "99.8%"},
		{db.Fact{Kind: db.FactDuration, Num: 273600}, "3d 4h"},
		{db.Fact{Kind: db.FactText, Text: "primary"}, "primary"},
		{db.Fact{Kind: db.FactText}, missing},
	}
	for _, c := range cases {
		if got := formatFact(c.f); got != c.want {
			t.Errorf("formatFact(%+v) = %q, want %q", c.f, got, c.want)
		}
	}
}

func TestMetricPoint(t *testing.T) {
	at := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	s := func(name string, counter bool, v float64, at time.Time) db.ServerSample {
		return db.ServerSample{Metrics: []db.Metric{{Name: name, Counter: counter, Value: v}}, At: at}
	}
	prev := s("QPS", true, 100, at)
	cases := []struct {
		name string
		prev *db.ServerSample
		cur  db.ServerSample
		want float64 // NaN: no value
	}{
		{"no previous sample", nil, s("QPS", true, 160, at.Add(2*time.Second)), math.NaN()},
		{"counter rate", &prev, s("QPS", true, 160, at.Add(2*time.Second)), 30},
		{"counter reset", &prev, s("QPS", true, 10, at.Add(2*time.Second)), math.NaN()},
		{"no time passed", &prev, s("QPS", true, 160, at), math.NaN()},
		{"gauge", &prev, s("QPS", false, 7, at.Add(2*time.Second)), 7},
		{"gauge without previous", nil, s("QPS", false, 7, at), 7},
		{"name changed", &prev, s("TPS", true, 160, at.Add(2*time.Second)), math.NaN()},
	}
	for _, c := range cases {
		got := metricPoint(c.prev, c.cur, 0)
		if math.IsNaN(c.want) != math.IsNaN(got) || !math.IsNaN(c.want) && got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSectionBar(t *testing.T) {
	names := []string{"Status", "Sessions", "Locks", "Slow queries", "Variables", "Replication"}
	full := ansi.Strip(sectionBar(names, 0, 80))
	for _, n := range names {
		if !strings.Contains(full, n) {
			t.Errorf("width 80 lacks %q: %q", n, full)
		}
	}
	if got := strings.TrimSpace(ansi.Strip(sectionBar(names, 2, 30))); got != "‹ Locks 3/6 ›" {
		t.Errorf("width 30 = %q", got)
	}
	if w := ansi.StringWidth(sectionBar(names, 5, 12)); w > 12 {
		t.Errorf("width 12 is %d wide", w)
	}
	if got := sectionBar(names, 0, 0); got != "" {
		t.Errorf("width 0 = %q", got)
	}
}

// openSQLServer opens the server tab of "local" (MySQL) from the tree with
// srv as its server and returns the held ticks.
func openSQLServer(t *testing.T, srv *fakeServer) (*Model, *fakeBackend, []tea.Msg) {
	t.Helper()
	m, b := newTestModel(t)
	b.conn.server = srv
	m.setFocus(focusTree)
	ticks := pressHold(t, m, "space", "i")
	if _, ok := m.activeTab().(*sqlServerTab); !ok {
		t.Fatalf("active tab = %T\n%s", m.activeTab(), screen(m))
	}
	return m, b, ticks
}

func countCalls(calls []string, name string) int {
	n := 0
	for _, c := range calls {
		if c == name {
			n++
		}
	}
	return n
}

func TestSQLServerTabOpens(t *testing.T) {
	srv := mysqlServer()
	m, _, ticks := openSQLServer(t, srv)
	if title := m.activeTab().Title(); title != "local · server" {
		t.Fatalf("title = %q", title)
	}
	if calls := srv.callList(); !slices.Equal(calls, []string{"Info", "Sample"}) {
		t.Fatalf("calls = %v", calls)
	}
	if len(ticks) != 1 {
		t.Fatalf("ticks = %d", len(ticks))
	}
	s := screen(m)
	for _, w := range []string{"Status", "Sessions", "Variables", "8.4.2", "connections", "87 / 500", "uptime", "3d 4h", "QPS", "running", "12"} {
		if !strings.Contains(s, w) {
			t.Errorf("screen lacks %q:\n%s", w, s)
		}
	}
	// the second sample turns the counters into rates: 60 questions in 2s
	sendHold(t, m, ticks...)
	s = screen(m)
	for _, w := range []string{"30", humanBytes(30*1024) + "/s"} {
		if !strings.Contains(s, w) {
			t.Errorf("after the second sample, screen lacks %q:\n%s", w, s)
		}
	}
}

func TestSQLServerSamplingStopsWhenHidden(t *testing.T) {
	m, b := newTestModel(t)
	srv := mysqlServer()
	b.conn.server = srv
	drain(t, m, m.openConsole("local", db.Namespace{Database: "app"}))
	m.setFocus(focusTree)
	ticks := pressHold(t, m, "space", "i")
	if _, ok := m.activeTab().(*sqlServerTab); !ok || len(ticks) != 1 {
		t.Fatalf("active %T, ticks %d", m.activeTab(), len(ticks))
	}
	// switching away: the pending tick does not sample
	pressHold(t, m, "g", "T")
	if held := sendHold(t, m, ticks...); len(held) != 0 || countCalls(srv.callList(), "Sample") != 1 {
		t.Fatalf("hidden: ticks %d, calls %v", len(held), srv.callList())
	}
	// switching back samples at once
	m.gotoTab(1)
	if held := drainHold(t, m, m.syncShown()); len(held) != 1 || countCalls(srv.callList(), "Sample") != 2 {
		t.Fatalf("shown again: ticks %d, calls %v", len(held), srv.callList())
	}
}

func TestSQLServerRefreshNoCatalog(t *testing.T) {
	srv := mysqlServer()
	m, b, _ := openSQLServer(t, srv)
	before := b.conn.catalogCount()
	pressHold(t, m, "space", "r")
	if n := b.conn.catalogCount(); n != before {
		t.Fatalf("catalog reads %d → %d", before, n)
	}
	if n := countCalls(srv.callList(), "Sample"); n != 2 {
		t.Fatalf("Sample calls = %d", n)
	}
}

func TestSQLServerSampleErrorThenRefresh(t *testing.T) {
	srv := mysqlServer()
	srv.sampleErr = fmt.Errorf("%w: connection reset by peer", db.ErrConnLost)
	m, _, ticks := openSQLServer(t, srv)
	if len(ticks) != 0 {
		t.Fatalf("still sampling after an error: %d ticks", len(ticks))
	}
	if s := screen(m); !strings.Contains(s, "connection reset by peer") {
		t.Fatalf("screen:\n%s", s)
	}
	srv.mu.Lock()
	srv.sampleErr = nil
	srv.mu.Unlock()
	if held := pressHold(t, m, "space", "r"); len(held) != 1 {
		t.Fatalf("Refresh: %d ticks", len(held))
	}
	if s := screen(m); strings.Contains(s, "connection reset by peer") || !strings.Contains(s, "87 / 500") {
		t.Fatalf("after Refresh:\n%s", s)
	}
}

func TestSQLServerUnavailableSample(t *testing.T) {
	srv := mysqlServer()
	srv.sampleErr = db.Unavailable("permission denied: needs PROCESS")
	m, _, _ := openSQLServer(t, srv)
	if s := screen(m); !strings.Contains(s, "permission denied: needs PROCESS") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestSQLServerNodeMenu(t *testing.T) {
	m, _ := newTestModel(t)
	m.setFocus(focusTree)
	for _, path := range [][]string{nil, {"app"}} {
		if path != nil {
			selectNode(t, m, "local", path...)
		}
		n := m.tree.Selected()
		if !slices.ContainsFunc(m.nodeMenu(n), func(it menuItem) bool { return it.desc == "Server status" }) {
			t.Errorf("local %v (%v): no Server status", path, n.Kind)
		}
	}
}

func TestSQLServerNoConnection(t *testing.T) {
	b := newFakeBackend()
	b.conns = nil
	m := newTestModelWith(t, b)
	m.setFocus(focusTree)
	press(t, m, "space", "i")
	if !strings.Contains(m.status, "select a connection") {
		t.Fatalf("status %q", m.status)
	}
}
