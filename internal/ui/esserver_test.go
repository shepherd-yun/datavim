package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/history"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

func TestESServerOverview(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.health = es.Health{Status: "yellow", Nodes: 3, DataNodes: 2, ActivePrimary: 10, Active: 17, Unassigned: 3, PendingTasks: 1}
	f.addIndex("orders", "", 5)
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindConn })
	press(t, m, "space", "i")
	tab, ok := m.activeTab().(*esServerTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	s := screen(m)
	for _, want := range []string{"8.11.0", "elasticsearch", "yellow", "3 unassigned shards", "nodes", "3 (2 data)", "pending tasks"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	tab.Hidden()
	n := len(f.sent())
	id := tab.id
	drain(t, m, func() tea.Msg { return esServerTickMsg{tabID: id, gen: tab.gen} })
	if len(f.sent()) != n {
		t.Errorf("hidden but read %v", f.sent()[n:])
	}
}

func TestSearchES(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", "", 1)
	f.addIndex("users", "", 1)
	f.aliases = []es.Alias{{Name: "orders-alias", Index: "orders"}}
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	search(t, m, "ord")
	s := screen(m)
	if !strings.Contains(s, "orders") || !strings.Contains(s, "orders-alias") || !strings.Contains(s, "alias") || strings.Contains(s, "users") {
		t.Fatalf("screen:\n%s", s)
	}
	pressAll(t, m, "enter")
	if tab, ok := m.activeTab().(*esIndexTab); !ok || tab.target != "orders" || tab.kind != targetIndex {
		t.Fatalf("active tab = %#v", m.activeTab())
	}
}

func TestRecentES(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", "", 1)
	f.aliases = []es.Alias{{Name: "orders-alias", Index: "orders"}}
	openESIndex(t, m, "orders-alias", targetAlias)
	drain(t, m, m.openESConsole("search"))
	for len(m.tabs) > 0 {
		drain(t, m, m.closeTab(0))
	}
	st := m.store.(*fakeStore)
	var kinds []string
	for _, r := range st.recents {
		kinds = append(kinds, r.Kind+":"+r.Name+":"+r.Database)
	}
	if !strings.Contains(strings.Join(kinds, " "), history.RecentESIndex+":orders-alias:alias") {
		t.Fatalf("recents %v", kinds)
	}
	for _, r := range slices.Clone(st.recents) {
		drain(t, m, m.openRecent(r))
	}
	var idx *esIndexTab
	var console bool
	for _, tb := range m.tabs {
		switch x := tb.(type) {
		case *esIndexTab:
			idx = x
		case *esConsoleTab:
			console = true
		}
	}
	if idx == nil || idx.kind != targetAlias || !console {
		t.Errorf("tabs %T idx %v console %v recents %v", m.tabs[0], idx != nil, console, kinds)
	}
}

// openESServerTab opens the cluster tab of the "search" connection.
func openESServerTab(t *testing.T, m *Model) *esServerTab {
	t.Helper()
	drain(t, m, m.openESServer("search"))
	tab, ok := m.activeTab().(*esServerTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	return tab
}

func twoNodes(f *fakeES) {
	f.nodes = []es.Node{
		{ID: "n1", Name: "es-1", IP: "10.0.0.1", Roles: "dimr", Master: true, HeapPct: 40, RAMPct: 50, CPUPct: 3, Load1m: 0.5, DiskUsedPct: 30, DiskUsed: 30 << 30, DiskTotal: 100 << 30, Version: "8.11.0"},
		{ID: "n2", Name: "es-2", IP: "10.0.0.2", Roles: "di", HeapPct: 90, RAMPct: 50, CPUPct: 3, Load1m: 0.5, DiskUsedPct: 88, DiskUsed: 88 << 30, DiskTotal: 100 << 30, Version: "8.11.0"},
	}
}

func TestESServerSections(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	twoNodes(f)
	tab := openESServerTab(t, m)
	n := len(f.sent())
	press(t, m, "]")
	if tab.section != secESNodes {
		t.Fatalf("section = %d", tab.section)
	}
	got := f.sent()[n:]
	for _, want := range []string{"GET _cluster/health", "GET _cat/nodes", "GET _cat/allocation", "GET _cluster/settings"} {
		if !slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, want) }) {
			t.Errorf("Nodes read %v, lacks %s", got, want)
		}
	}
	for _, s := range got {
		if strings.HasPrefix(s, "GET _cat/shards") || s == "GET " || strings.HasPrefix(s, "GET _cluster/stats") {
			t.Errorf("Nodes read %s", s)
		}
	}
	if s := screen(m); !strings.Contains(s, "es-1") || !strings.Contains(s, "HEAP %") || !strings.Contains(s, "★") {
		t.Errorf("screen:\n%s", s)
	}
	// the watermark is read once
	n = len(f.sent())
	drain(t, m, tab.Handle(keymap.Refresh))
	for _, s := range f.sent()[n:] {
		if strings.HasPrefix(s, "GET _cluster/settings") {
			t.Errorf("settings read again: %v", f.sent()[n:])
		}
	}
	for _, want := range []eserverSection{secESShards, secESAllocation, secESTasks, secESPending, secESSettings} {
		press(t, m, "]")
		if tab.section != want {
			t.Fatalf("section = %d, want %d", tab.section, want)
		}
	}
	if tab.Context() != keymap.ESClusterSettings {
		t.Errorf("context = %s", tab.Context())
	}
	press(t, m, "[")
	if tab.section != secESPending || tab.Context() != keymap.ESServer {
		t.Errorf("section %d context %s", tab.section, tab.Context())
	}
	if s := screen(m); !strings.Contains(s, "no pending cluster tasks") {
		t.Errorf("screen:\n%s", s)
	}
	f.pending = []es.PendingTask{{Order: 3, Priority: "HIGH", Source: "shard-started", Waiting: "5ms"}}
	drain(t, m, tab.Handle(keymap.Refresh))
	if s := screen(m); !strings.Contains(s, "shard-started") || !strings.Contains(s, "PRIORITY") {
		t.Errorf("screen:\n%s", s)
	}
	press(t, m, "[", "[")
	if s := screen(m); !strings.Contains(s, "DISK AVAIL") || !strings.Contains(s, "es-2") {
		t.Errorf("allocation screen:\n%s", s)
	}
}

func TestESServerNodesWarn(t *testing.T) {
	for _, tc := range []struct {
		watermark  string
		heap, disk int
		warn       bool
	}{
		{"", 90, 10, true},
		{"85%", 10, 88, true},
		{"", 10, 88, false},
		{"85%", 10, 80, false},
	} {
		m, _, f := newESModel(t, config.Connection{})
		f.nodes = []es.Node{{ID: "n1", Name: "es-1", HeapPct: tc.heap, DiskUsedPct: float64(tc.disk), DiskUsed: int64(tc.disk) << 30, DiskTotal: 100 << 30}}
		if tc.watermark != "" {
			f.csettings = map[string]map[string]any{"defaults": {"cluster.routing.allocation.disk.watermark.high": tc.watermark}}
		}
		tab := openESServerTab(t, m)
		drain(t, m, tab.setSection(secESNodes))
		if got := tab.nodeMark(0, 0) == grid.MarkError; got != tc.warn {
			t.Errorf("%+v: warn = %v", tc, got)
		}
	}
}

func TestESServerOverviewJump(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.health.Status = "yellow"
	tab := openESServerTab(t, m)
	press(t, m, "enter")
	if tab.section != secESShards {
		t.Errorf("yellow: section = %d", tab.section)
	}
	f.health.Status = "green"
	drain(t, m, tab.setSection(secESOverview))
	press(t, m, "enter")
	if tab.section != secESOverview {
		t.Errorf("green: section = %d", tab.section)
	}
}

func TestESServerHiddenStopsSections(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	twoNodes(f)
	tab := openESServerTab(t, m)
	drain(t, m, tab.setSection(secESNodes))
	tab.Hidden()
	n := len(f.sent())
	id := tab.id
	drain(t, m, func() tea.Msg { return esServerTickMsg{tabID: id, gen: tab.gen} })
	if len(f.sent()) != n {
		t.Errorf("hidden but read %v", f.sent()[n:])
	}
}

func fourShards(f *fakeES) {
	f.shards = []es.Shard{
		{Index: "orders", Shard: 0, Primary: true, State: "STARTED", Node: "es-1", Docs: 10, Bytes: 1000},
		{Index: "orders", Shard: 0, State: "UNASSIGNED", Docs: -1, Bytes: -1, Reason: "INDEX_CREATED"},
		{Index: "logs-1", Shard: 0, Primary: true, State: "STARTED", Node: "es-1", Docs: 5, Bytes: 500},
		{Index: "logs-2", Shard: 0, Primary: true, State: "STARTED", Node: "es-1", Docs: 5, Bytes: 500},
	}
}

func TestESServerShardsDefault(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fourShards(f)
	tab := openESServerTab(t, m)
	drain(t, m, tab.setSection(secESShards))
	if got := tab.grids[secESShards].Result().Rows; len(got) != 1 || got[0][3].Text != "UNASSIGNED" {
		t.Fatalf("rows = %v", got)
	}
	press(t, m, ".")
	if got := tab.grids[secESShards].Result().Rows; len(got) != 4 {
		t.Fatalf("all rows = %v", got)
	}
	f.shards = f.shards[:1]
	press(t, m, ".")
	drain(t, m, tab.Handle(keymap.Refresh))
	if s := screen(m); !strings.Contains(s, "every shard is started") {
		t.Errorf("screen:\n%s", s)
	}
}

func TestESServerShardsFilter(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fourShards(f)
	tab := openESServerTab(t, m)
	drain(t, m, tab.setSection(secESShards))
	press(t, m, ".", "F")
	typeText(t, m, "logs-*")
	press(t, m, "enter")
	if got := tab.grids[secESShards].Result().Rows; len(got) != 2 || got[0][0].Text != "logs-1" {
		t.Fatalf("filtered = %v", got)
	}
	if s := screen(m); !strings.Contains(s, "filter logs-*") {
		t.Errorf("screen:\n%s", s)
	}
	press(t, m, "f", "x")
	if got := tab.grids[secESShards].Result().Rows; len(got) != 4 {
		t.Fatalf("cleared = %v", got)
	}
}

func TestESServerExplain(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fourShards(f)
	tab := openESServerTab(t, m)
	drain(t, m, tab.setSection(secESShards))
	press(t, m, "enter")
	if b := f.lastBody("POST _cluster/allocation/explain"); b != `{"index":"orders","shard":0,"primary":false}` {
		t.Fatalf("explain body = %q", b)
	}
	d, ok := m.overlay.(*overlay.Detail)
	if !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	if s := ansi.Strip(d.View(100, 30)); !strings.Contains(s, "same_shard") || !strings.Contains(s, "orders[0] r") {
		t.Errorf("detail:\n%s", s)
	}
	press(t, m, "esc", ".")
	n := len(f.sent())
	press(t, m, "g", "g", "enter")
	if !strings.Contains(tab.status, "only unassigned shards have an allocation explanation") {
		t.Errorf("status = %q", tab.status)
	}
	for _, s := range f.sent()[n:] {
		if strings.Contains(s, "explain") {
			t.Errorf("sent %s", s)
		}
	}
}

func TestESServerNodeDetail(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	twoNodes(f)
	tab := openESServerTab(t, m)
	drain(t, m, tab.setSection(secESNodes))
	press(t, m, "j", "enter")
	if !slices.ContainsFunc(f.sent(), func(s string) bool { return strings.HasPrefix(s, "GET _nodes/n2/stats/") }) {
		t.Fatalf("sent %v", f.sent())
	}
	d, ok := m.overlay.(*overlay.Detail)
	if !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	if s := ansi.Strip(d.View(100, 30)); !strings.Contains(s, "heap") || !strings.Contains(s, "es-2") {
		t.Errorf("detail:\n%s", s)
	}
}

func TestOpenESShards(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fourShards(f)
	drain(t, m, m.openESShards("search", "orders"))
	tab, ok := m.activeTab().(*esServerTab)
	if !ok || tab.section != secESShards || tab.shardFilter != "orders" || !tab.shardsAll {
		t.Fatalf("tab = %#v", m.activeTab())
	}
	if got := tab.grids[secESShards].Result().Rows; len(got) != 2 {
		t.Errorf("rows = %v", got)
	}
	if !slices.Contains(f.sent(), "GET _cat/shards/orders?format=json&bytes=b&h=index,shard,prirep,state,docs,store,node,unassigned.reason") {
		t.Errorf("sent %v", f.sent())
	}
}

func twoTasks(f *fakeES) {
	f.tasks = []es.Task{
		{ID: "n1:7", Node: "n1", Action: "indices:data/write/reindex", Description: "reindex from [orders] to [orders-v2]", Running: 90 * time.Second, Cancellable: true, Total: 88000, Done: 12000},
		{ID: "n1:9", Node: "n1", Action: "indices:admin/forcemerge", Description: "Force-merge indices [logs]", Running: time.Second, Total: -1, Done: -1},
	}
}

func TestESServerTasks(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	tab := openESServerTab(t, m)
	drain(t, m, tab.setSection(secESTasks))
	if s := screen(m); !strings.Contains(s, "no long-running tasks") {
		t.Errorf("screen:\n%s", s)
	}
	twoTasks(f)
	drain(t, m, tab.Handle(keymap.Refresh))
	rows := tab.grids[secESTasks].Result().Rows
	if len(rows) != 2 || rows[0][2].Text != "12,000 / 88,000 (13%)" || rows[1][2].Text != "" || rows[0][5].Text != "n1:7" {
		t.Errorf("rows = %v", rows)
	}
}

func TestESServerCancelTask(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	twoTasks(f)
	tab := openESServerTab(t, m)
	drain(t, m, tab.setSection(secESTasks))
	press(t, m, "d", "d")
	if _, ok := m.overlay.(*overlay.Confirm); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	press(t, m, "y")
	if !slices.Contains(f.sent(), "POST _tasks/n1:7/_cancel") {
		t.Fatalf("sent %v", f.sent())
	}
	if tab.status != "cancelled n1:7" || tab.statusE {
		t.Errorf("status = %q", tab.status)
	}
	press(t, m, "j", "d", "d")
	if m.overlay != nil || tab.status != "this task cannot be cancelled" {
		t.Errorf("overlay %T status %q", m.overlay, tab.status)
	}
}

func TestESServerCancelGone(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	twoTasks(f)
	f.routes["POST _tasks/n1:7/_cancel"] = func(es.Request) es.Response {
		return errResp(404, "resource_not_found_exception", "task [n1:7] is not found")
	}
	tab := openESServerTab(t, m)
	drain(t, m, tab.setSection(secESTasks))
	press(t, m, "d", "d", "y")
	if tab.status != "task already finished" || tab.statusE {
		t.Errorf("status = %q (err %v)", tab.status, tab.statusE)
	}
	sent := f.sent()
	if last := sent[len(sent)-1]; !strings.HasPrefix(last, "GET _tasks?") {
		t.Errorf("not refreshed: %v", sent)
	}
}

func TestESServerCancelReadOnly(t *testing.T) {
	ro := true
	m, _, f := newESModel(t, config.Connection{ReadOnly: &ro})
	twoTasks(f)
	tab := openESServerTab(t, m)
	drain(t, m, tab.setSection(secESTasks))
	n := len(f.sent())
	press(t, m, "d", "d")
	if m.overlay != nil || tab.status != readOnlyHint || len(f.sent()) != n {
		t.Errorf("overlay %T status %q sent %v", m.overlay, tab.status, f.sent()[n:])
	}
}

func TestOpenESTasks(t *testing.T) {
	m, _, _ := newESModel(t, config.Connection{})
	drain(t, m, m.openESTasks("search"))
	if tab, ok := m.activeTab().(*esServerTab); !ok || tab.section != secESTasks {
		t.Fatalf("tab = %#v", m.activeTab())
	}
}

func TestESServerShardsFilterPartialName(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fourShards(f)
	f.routes["GET _cat/shards/ord"] = func(es.Request) es.Response {
		return errResp(404, "index_not_found_exception", "no such index [ord]")
	}
	tab := openESServerTab(t, m)
	drain(t, m, tab.setSection(secESShards))
	press(t, m, ".", "F")
	typeText(t, m, "ord")
	press(t, m, "enter")
	if tab.err != "" {
		t.Fatalf("err = %q", tab.err)
	}
	if got := tab.grids[secESShards].Result().Rows; len(got) != 2 || got[0][0].Text != "orders" {
		t.Errorf("rows = %v", got)
	}
}
