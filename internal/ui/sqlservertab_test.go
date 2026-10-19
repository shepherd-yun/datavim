package ui

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
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

// withSections fills srv's table sections.
func withSections(srv *fakeServer) *fakeServer {
	srv.sessions = db.SessionList{
		Rows: textResult([]string{"id", "user", "state", "time", "sql"}, [][]string{
			{"48211", "app", "executing", "4m12s", "SELECT\n  1"},
			{"50117", "", "Sleep", "3s", ""},
		}),
		IDs: []db.SessionID{{ID: "48211", Since: "t0"}, {ID: "50117"}},
		Info: []db.SessionSummary{
			{User: "app", Host: "10.0.3.21", DB: "shop", State: "executing", Elapsed: "4m12s", SQL: "SELECT\n  1"},
			{State: "Sleep", Elapsed: "3s"},
		},
		Self:  []bool{false, true},
		Total: 2, Active: 1,
	}
	srv.sets = map[string]*db.ResultSet{
		"locks":       textResult([]string{"waiting", "blocked by"}, [][]string{{"48230", "48211"}}),
		"top":         textResult([]string{"statement", "calls"}, [][]string{{"SELECT ?", "12"}}),
		"settings":    textResult([]string{"name", "value"}, [][]string{{"max_connections", "500"}, {"Max_Allowed_Packet", "64M"}, {"wait_timeout", "28800"}}),
		"replication": textResult([]string{"direction", "peer"}, nil),
	}
	return srv
}

func TestSQLServerSections(t *testing.T) {
	srv := withSections(mysqlServer())
	srv.info.SettingsTitle = "Parameters"
	m, _, _ := openSQLServer(t, srv)
	for i, want := range []string{"Sessions", "Locks", "TopStatements", "Settings", "Replication"} {
		pressHold(t, m, "]")
		if calls := srv.callList(); calls[len(calls)-1] != want {
			t.Fatalf("section %d: calls %v", i+1, calls)
		}
	}
	s := screen(m)
	if !strings.Contains(s, "Parameters") || !strings.Contains(s, "no replication configured") {
		t.Fatalf("screen:\n%s", s)
	}
	samples := countCalls(srv.callList(), "Sample")
	if held := pressHold(t, m, "]"); len(held) != 1 || countCalls(srv.callList(), "Sample") != samples+1 {
		t.Fatalf("back to Status: ticks %d, calls %v", len(held), srv.callList())
	}
	// back to Replication reads it again, and so does Refresh
	pressHold(t, m, "[")
	pressHold(t, m, "space", "r")
	if n := countCalls(srv.callList(), "Replication"); n != 3 {
		t.Fatalf("Replication calls = %d", n)
	}
}

func TestSQLServerStaleSectionDropped(t *testing.T) {
	srv := withSections(mysqlServer())
	m, _, _ := openSQLServer(t, srv)
	_, load := m.Update(uitest.Key("]")) // Sessions, not run yet
	pressHold(t, m, "]")                 // Locks
	drainHold(t, m, load)
	tab := m.activeTab().(*sqlServerTab)
	if tab.section != sqlLocks || tab.gridSec != sqlLocks {
		t.Fatalf("section %d, grid holds %d", tab.section, tab.gridSec)
	}
	if s := screen(m); strings.Contains(s, "48211 ") && !strings.Contains(s, "48230") {
		t.Fatalf("sessions under Locks:\n%s", s)
	}
}

func TestSQLServerUnavailableSection(t *testing.T) {
	srv := withSections(mysqlServer())
	srv.errs = map[string]error{"top": db.Unavailable(`pg_stat_statements is not installed in database "shop"`)}
	m, _, _ := openSQLServer(t, srv)
	pressHold(t, m, "]", "]", "]")
	if s := screen(m); !strings.Contains(s, `pg_stat_statements is not installed in database "shop"`) {
		t.Fatalf("screen:\n%s", s)
	}
	pressHold(t, m, "]")
	if s := screen(m); !strings.Contains(s, "max_connections") {
		t.Fatalf("settings after an unavailable section:\n%s", s)
	}
}

func TestSQLServerSessions(t *testing.T) {
	srv := withSections(mysqlServer())
	m, _, _ := openSQLServer(t, srv)
	pressHold(t, m, "]")
	s := screen(m)
	for _, w := range []string{"2 sessions · 1 running · idle hidden (a)", "* sqlvim", "*50117", "SELECT↵  1"} {
		if !strings.Contains(s, w) {
			t.Errorf("screen lacks %q:\n%s", w, s)
		}
	}
	pressHold(t, m, "a")
	if calls := srv.callList(); calls[len(calls)-1] != "Sessions idle" {
		t.Fatalf("calls %v", calls)
	}
	if s := screen(m); !strings.Contains(s, "idle shown (a)") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestSQLServerSettingsFilter(t *testing.T) {
	srv := withSections(mysqlServer())
	m, _, _ := openSQLServer(t, srv)
	pressHold(t, m, "]", "]", "]", "]")
	pressHold(t, m, "F", "m", "a", "x", "enter")
	tab := m.activeTab().(*sqlServerTab)
	if n := tab.grid.RowCount(); n != 2 {
		t.Fatalf("filtered rows = %d\n%s", n, screen(m))
	}
	if s := screen(m); !strings.Contains(s, "filter: max") {
		t.Fatalf("screen:\n%s", s)
	}
	pressHold(t, m, "f", "x")
	if n := tab.grid.RowCount(); n != 3 {
		t.Fatalf("rows after clearing = %d", n)
	}
	// the filter is the settings section's only
	pressHold(t, m, "[")
	pressHold(t, m, "F")
	if !strings.Contains(m.activeTab().(*sqlServerTab).status, "variables") {
		t.Fatalf("F on Slow queries: status %q", m.activeTab().(*sqlServerTab).status)
	}
}

// openSessions opens the server tab of "local" configured as cfg (name and
// driver kept) on its Sessions section.
func openSessions(t *testing.T, srv *fakeServer, cfg config.Connection) *Model {
	t.Helper()
	m, b := newTestModel(t)
	b.mu.Lock()
	cfg.Name, cfg.Driver = "local", "mysql"
	b.conns[0] = cfg
	b.mu.Unlock()
	b.conn.server = srv
	m.setFocus(focusTree)
	pressHold(t, m, "space", "i")
	pressHold(t, m, "]")
	return m
}

func tabStatus(m *Model) string { return m.activeTab().(*sqlServerTab).status }

func TestSQLServerKillReadOnly(t *testing.T) {
	srv := withSections(mysqlServer())
	m := openSessions(t, srv, config.Connection{ReadOnly: boolPtr(true)})
	pressHold(t, m, "d", "d")
	if s := tabStatus(m); s != "read-only connection: cannot cancel or disconnect sessions" || m.overlay != nil {
		t.Fatalf("status %q, overlay %T", s, m.overlay)
	}
	pressHold(t, m, "c")
	if len(srv.killed)+len(srv.cancelled) != 0 {
		t.Fatalf("killed %v, cancelled %v", srv.killed, srv.cancelled)
	}
}

func TestSQLServerKillConfirm(t *testing.T) {
	srv := withSections(mysqlServer())
	m := openSessions(t, srv, config.Connection{Env: config.EnvDev})
	pressHold(t, m, "d", "d")
	if s := screen(m); !strings.Contains(s, "Disconnect session 48211?") || !strings.Contains(s, "user app · host 10.0.3.21 · db shop") {
		t.Fatalf("screen:\n%s", s)
	}
	pressHold(t, m, "y")
	if !slices.Equal(srv.killed, []db.SessionID{{ID: "48211", Since: "t0"}}) {
		t.Fatalf("killed %v", srv.killed)
	}
	if s := tabStatus(m); s != "disconnected session 48211" {
		t.Fatalf("status %q", s)
	}
	if calls := srv.callList(); calls[len(calls)-1] != "Sessions" {
		t.Fatalf("not reloaded: %v", calls)
	}
	// cancel on the same row
	pressHold(t, m, "c", "y")
	if len(srv.cancelled) != 1 || tabStatus(m) != "cancelled the query of session 48211" {
		t.Fatalf("cancelled %v, status %q", srv.cancelled, tabStatus(m))
	}
}

func TestSQLServerKillProdTypesName(t *testing.T) {
	srv := withSections(mysqlServer())
	m := openSessions(t, srv, config.Connection{Env: config.EnvProd, ReadOnly: boolPtr(false)})
	pressHold(t, m, "d", "d", "y")
	if len(srv.killed) != 0 {
		t.Fatalf("y killed on prod: %v", srv.killed)
	}
	pressHold(t, m, "backspace", "l", "o", "c", "a", "l", "enter")
	if len(srv.killed) != 1 {
		t.Fatalf("killed %v\n%s", srv.killed, screen(m))
	}
}

func TestSQLServerCancelUnsupported(t *testing.T) {
	srv := withSections(mysqlServer())
	srv.info.CanCancel = false
	m := openSessions(t, srv, config.Connection{Env: config.EnvDev})
	pressHold(t, m, "c")
	if s := tabStatus(m); !strings.Contains(s, "not supported") || !strings.Contains(s, "dd to disconnect") || m.overlay != nil {
		t.Fatalf("status %q, overlay %T", s, m.overlay)
	}
}

func TestSQLServerKillChanged(t *testing.T) {
	srv := withSections(mysqlServer())
	srv.errs = map[string]error{"kill": db.ErrSessionChanged}
	m := openSessions(t, srv, config.Connection{Env: config.EnvDev})
	n := countCalls(srv.callList(), "Sessions")
	pressHold(t, m, "d", "d", "y")
	if s := tabStatus(m); s != "session 48211 changed since the list was read; refreshed" {
		t.Fatalf("status %q", s)
	}
	if countCalls(srv.callList(), "Sessions") != n+1 {
		t.Fatalf("not reloaded: %v", srv.callList())
	}
}

func TestSQLServerKillGone(t *testing.T) {
	srv := withSections(mysqlServer())
	srv.gone = true
	m := openSessions(t, srv, config.Connection{Env: config.EnvDev})
	pressHold(t, m, "d", "d", "y")
	if s := tabStatus(m); s != "session 48211 is already gone" {
		t.Fatalf("status %q", s)
	}
}

func TestSQLServerKillOutsideSessions(t *testing.T) {
	srv := withSections(mysqlServer())
	m := openSessions(t, srv, config.Connection{Env: config.EnvDev})
	pressHold(t, m, "]") // Locks
	pressHold(t, m, "d", "d")
	if s := tabStatus(m); s != "works in the sessions list" || m.overlay != nil {
		t.Fatalf("status %q", s)
	}
}

func TestSQLServerKillSelfWarns(t *testing.T) {
	srv := withSections(mysqlServer())
	m := openSessions(t, srv, config.Connection{Env: config.EnvDev})
	pressHold(t, m, "j", "d", "d")
	if s := screen(m); !strings.Contains(s, "This is a sqlvim connection (maybe this one).") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestSQLServerInfoError(t *testing.T) {
	srv := withSections(mysqlServer())
	srv.infoErr = errors.New("ORA-00942: table or view does not exist")
	m := openSessions(t, srv, config.Connection{Env: config.EnvDev})
	if s := screen(m); !strings.Contains(s, "ORA-00942: table or view does not exist") {
		t.Fatalf("Info error not shown:\n%s", s)
	}
	// without Info sqlvim cannot tell whether cancelling works
	pressHold(t, m, "c")
	if s := tabStatus(m); strings.Contains(s, "not supported") || !strings.Contains(s, "server info") || m.overlay != nil {
		t.Fatalf("c without Info: status %q, overlay %T", s, m.overlay)
	}
	// Refresh reads Info again
	srv.mu.Lock()
	srv.infoErr = nil
	srv.mu.Unlock()
	pressHold(t, m, "space", "r")
	if s := screen(m); strings.Contains(s, "ORA-00942") {
		t.Fatalf("after Refresh:\n%s", s)
	}
	pressHold(t, m, "c")
	if m.overlay == nil {
		t.Fatalf("c after Info: status %q", tabStatus(m))
	}
}
