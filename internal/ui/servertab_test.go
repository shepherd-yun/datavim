package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

// replyErr is a server error reply: it has go-redis's RedisError marker,
// so rdb.IsNoPerm recognizes a NOPERM one.
type replyErr string

func (e replyErr) Error() string { return string(e) }
func (replyErr) RedisError()     {}

const noPerm = replyErr("NOPERM this user has no permissions to run the 'info' command")

func redis7Info() rdb.Info {
	return rdb.ParseInfo("# Server\nredis_version:7.2.4\nuptime_in_seconds:273600\n" +
		"# Clients\nconnected_clients:12\nblocked_clients:0\n" +
		"# Memory\nused_memory:1048576\nused_memory_human:1.00M\nused_memory_peak_human:2.00M\nmaxmemory:0\nmaxmemory_human:0B\nmaxmemory_policy:noeviction\n" +
		"# Stats\ninstantaneous_ops_per_sec:120\ninstantaneous_input_kbps:1.5\ninstantaneous_output_kbps:12.25\nkeyspace_hits:982\nkeyspace_misses:18\nevicted_keys:0\nexpired_keys:7\n" +
		"# Replication\nrole:master\nconnected_slaves:2\n" +
		"# Keyspace\ndb0:keys=1234,expires=0,avg_ttl=0\n")
}

// drainHold is drainAll that holds back the server tab's ticks instead of
// feeding them in (they would sample forever); it returns them.
func drainHold(t *testing.T, m *Model, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	var held []tea.Msg
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 10_000 {
			t.Fatal("drainHold: too many steps")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case serverTickMsg:
			held = append(held, msg)
		default:
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
	return held
}

func pressHold(t *testing.T, m *Model, keys ...string) []tea.Msg {
	t.Helper()
	var held []tea.Msg
	for _, k := range keys {
		_, cmd := m.Update(uitest.Key(k))
		held = append(held, drainHold(t, m, cmd)...)
	}
	return held
}

func sendHold(t *testing.T, m *Model, msgs ...tea.Msg) []tea.Msg {
	t.Helper()
	var held []tea.Msg
	for _, msg := range msgs {
		_, cmd := m.Update(msg)
		held = append(held, drainHold(t, m, cmd)...)
	}
	return held
}

func infoCalls(r *fakeRedis) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.infoCalls
}

// openServerTab opens the server tab of "cache" from the tree and returns
// the first tick.
func openServerTab(t *testing.T, cfg config.Connection) (*Model, *fakeRedis, []tea.Msg) {
	t.Helper()
	m, _, r := newRedisModel(t, cfg)
	r.info = redis7Info()
	m.setFocus(focusTree)
	ticks := pressHold(t, m, "space", "i")
	if _, ok := m.activeTab().(*serverTab); !ok {
		t.Fatalf("active tab = %T\n%s", m.activeTab(), screen(m))
	}
	return m, r, ticks
}

func TestStatusFacts(t *testing.T) {
	got := map[string]string{}
	for _, f := range statusFacts(redis7Info()) {
		got[f.label] = f.value
	}
	want := map[string]string{
		"version":  "7.2.4",
		"role":     "master · 2 replicas",
		"uptime":   "3d 4h",
		"clients":  "12",
		"keys":     "1,234",
		"hit rate": "98.2%",
		"evicted":  "0",
		"expired":  "7",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if m := got["memory"]; !strings.Contains(m, "1.00M") || !strings.Contains(m, "peak 2.00M") || !strings.Contains(m, "max no limit") || !strings.Contains(m, "(noeviction)") {
		t.Errorf("memory = %q", m)
	}

	replica := rdb.Info{"role": "slave", "master_host": "10.0.0.1", "master_port": "6379", "master_link_status": "up", "blocked_clients": "2", "connected_clients": "5"}
	got = map[string]string{}
	for _, f := range statusFacts(replica) {
		got[f.label] = f.value
	}
	if got["role"] != "slave of 10.0.0.1:6379 · link up" || got["clients"] != "5 · 2 blocked" {
		t.Errorf("replica: %v", got)
	}
}

func TestUptime(t *testing.T) {
	for s, want := range map[int64]string{273600: "3d 4h", 15120: "4h 12m", 725: "12m 5s", 9: "9s"} {
		if got := uptime(s); got != want {
			t.Errorf("uptime(%d) = %q, want %q", s, got, want)
		}
	}
}

func TestStatusFactsMissing(t *testing.T) {
	facts := statusFacts(rdb.Info{})
	if len(facts) != 9 {
		t.Fatalf("facts = %v", facts)
	}
	for _, f := range facts {
		if f.value != "—" {
			t.Errorf("%s = %q, want —", f.label, f.value)
		}
	}
}

func TestServerTabOpens(t *testing.T) {
	m, r, ticks := openServerTab(t, config.Connection{})
	if title := m.activeTab().Title(); title != "cache · server" {
		t.Fatalf("title = %q", title)
	}
	if n := infoCalls(r); n != 1 {
		t.Fatalf("infoCalls = %d", n)
	}
	if len(ticks) != 1 {
		t.Fatalf("ticks = %d", len(ticks))
	}
	s := screen(m)
	for _, w := range []string{"Status", "Slow log", "Clients", "7.2.4", "master · 2 replicas", "ops/sec", "120", "net out", "12.2 KB/s"} {
		if !strings.Contains(s, w) {
			t.Errorf("screen lacks %q:\n%s", w, s)
		}
	}

	m2, _ := newTestModel(t)
	m2.setFocus(focusTree)
	press(t, m2, "space", "i")
	if !strings.Contains(m2.status, "select a Redis connection") {
		t.Fatalf("SQL connection: status %q", m2.status)
	}
}

func TestServerSamplingTicks(t *testing.T) {
	m, r, ticks := openServerTab(t, config.Connection{})
	ticks = sendHold(t, m, ticks...)
	if n := infoCalls(r); n != 2 {
		t.Fatalf("infoCalls = %d", n)
	}
	if len(ticks) != 1 {
		t.Fatalf("next ticks = %d", len(ticks))
	}
	if n := len(m.activeTab().(*serverTab).samples); n != 2 {
		t.Fatalf("samples = %d", n)
	}
}

func TestServerSamplingKeepsLast(t *testing.T) {
	m, _, ticks := openServerTab(t, config.Connection{})
	for range statusSamples + 5 {
		ticks = sendHold(t, m, ticks...)
	}
	if n := len(m.activeTab().(*serverTab).samples); n != statusSamples {
		t.Fatalf("samples = %d", n)
	}
}

func TestServerSamplingStopsWhenHidden(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.info = redis7Info()
	r.put(0, "k", fakeValue{typ: "string", str: "v"})
	drainAll(t, m, m.openKey("cache", 0, "k"))
	ticks := pressHold(t, m, "space", "i")
	if _, ok := m.activeTab().(*serverTab); !ok || infoCalls(r) != 1 {
		t.Fatalf("active %T, infoCalls %d", m.activeTab(), infoCalls(r))
	}
	// switching away: the pending tick does not sample
	if held := pressHold(t, m, "g", "T"); len(held) != 0 {
		t.Fatalf("ticks after switching away: %d", len(held))
	}
	if held := sendHold(t, m, ticks...); len(held) != 0 || infoCalls(r) != 1 {
		t.Fatalf("hidden: ticks %d, infoCalls %d", len(held), infoCalls(r))
	}
	// switching back samples at once
	ticks = pressHold(t, m, "g", "t")
	if infoCalls(r) != 2 || len(ticks) != 1 {
		t.Fatalf("shown again: infoCalls %d, ticks %d", infoCalls(r), len(ticks))
	}
	// the slow log section does not sample
	pressHold(t, m, "]")
	if held := sendHold(t, m, ticks...); len(held) != 0 || infoCalls(r) != 2 {
		t.Fatalf("slow log: ticks %d, infoCalls %d", len(held), infoCalls(r))
	}
	ticks = pressHold(t, m, "[")
	if infoCalls(r) != 3 || len(ticks) != 1 {
		t.Fatalf("back to status: infoCalls %d, ticks %d", infoCalls(r), len(ticks))
	}
	// opening another tab hides it too
	sendHold(t, m, openKeyMsg{conn: "cache", db: 0, key: "k"})
	if _, ok := m.activeTab().(*keyTab); !ok {
		t.Fatalf("active %T", m.activeTab())
	}
	if held := sendHold(t, m, ticks...); len(held) != 0 || infoCalls(r) != 3 {
		t.Fatalf("other tab opened: ticks %d, infoCalls %d", len(held), infoCalls(r))
	}
	// and closing it stops it for good
	ticks = pressHold(t, m, "g", "t")
	if infoCalls(r) != 4 {
		t.Fatalf("infoCalls %d", infoCalls(r))
	}
	m.Update(closeTabConfirmedMsg{tabID: m.activeTab().ID()})
	if held := sendHold(t, m, ticks...); len(held) != 0 || infoCalls(r) != 4 {
		t.Fatalf("closed: ticks %d, infoCalls %d", len(held), infoCalls(r))
	}
}

func TestServerSamplingNoDoubleTick(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.info = redis7Info()
	r.put(0, "k", fakeValue{typ: "string", str: "v"})
	drainAll(t, m, m.openKey("cache", 0, "k"))
	old := pressHold(t, m, "space", "i")
	pressHold(t, m, "g", "T")
	fresh := pressHold(t, m, "g", "t")
	if infoCalls(r) != 2 {
		t.Fatalf("infoCalls %d", infoCalls(r))
	}
	next := sendHold(t, m, append(old, fresh...)...)
	if infoCalls(r) != 3 || len(next) != 1 {
		t.Fatalf("old and new tick: infoCalls %d, ticks %d", infoCalls(r), len(next))
	}
}

func TestServerStatusNoPerm(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.infoErr = noPerm
	m.setFocus(focusTree)
	ticks := pressHold(t, m, "space", "i")
	if len(ticks) != 0 {
		t.Fatalf("ticks = %d", len(ticks))
	}
	if s := screen(m); !strings.Contains(s, "permission denied") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestServerStatusRefresh(t *testing.T) {
	m, r, _ := openServerTab(t, config.Connection{})
	r.mu.Lock()
	r.infoErr = replyErr("ERR unknown command 'INFO'")
	r.mu.Unlock()
	if held := pressHold(t, m, "space", "r"); len(held) != 0 {
		t.Fatalf("error: ticks %d", len(held))
	}
	if s := screen(m); !strings.Contains(s, "ERR unknown command") {
		t.Fatalf("screen:\n%s", s)
	}
	r.mu.Lock()
	r.infoErr = nil
	r.mu.Unlock()
	if held := pressHold(t, m, "space", "r"); len(held) != 1 {
		t.Fatalf("refresh restarts: ticks %d", len(held))
	}
	if n := len(m.activeTab().(*serverTab).samples); n != 1 {
		t.Fatalf("samples after refresh = %d", n)
	}
}
