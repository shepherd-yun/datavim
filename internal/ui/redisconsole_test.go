package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/history"
	"github.com/shepherd-yun/datavim/internal/rdb"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// openRConsole opens the redis console on db n of cache, in NORMAL mode.
func openRConsole(t *testing.T, m *Model, n int) *redisConsoleTab {
	t.Helper()
	drainAll(t, m, m.openRedisConsole("cache", n))
	c, ok := m.activeTab().(*redisConsoleTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	c.editor.Escape()
	return c
}

// runR puts text in the console and runs action a.
func runR(t *testing.T, m *Model, c *redisConsoleTab, text string, a keymap.Action) {
	t.Helper()
	c.editor.SetValue(text)
	drainAll(t, m, c.Handle(a))
}

// sent is what the console's sessions received, without PING.
func sent(b *fakeBackend) []string {
	var out []string
	b.mu.Lock()
	sessions := slices.Clone(b.sessions)
	b.mu.Unlock()
	for _, s := range sessions {
		for _, c := range s.commands() {
			if c != "PING" {
				out = append(out, c)
			}
		}
	}
	return out
}

func TestRedisConsoleOpen(t *testing.T) {
	m, b, r := newRedisModel(t, config.Connection{Database: "2"})
	r.fill(3, "k", 1)
	m.setFocus(focusTree)
	pressAll(t, m, "l", "j")
	if n := m.tree.Selected(); n == nil || n.Kind != tree.KindRedisDB || n.Name != "db3" {
		t.Fatalf("selected %+v", m.tree.Selected())
	}
	pressAll(t, m, "space", "c")
	c, ok := m.activeTab().(*redisConsoleTab)
	if !ok || c.Title() != "redis:cache/db3" {
		t.Fatalf("tab = %T %v", m.activeTab(), m.activeTab())
	}
	if b.connCalls != 0 {
		t.Fatalf("Conn calls = %d", b.connCalls)
	}
	if !strings.Contains(screen(m), "redis:cache/db3") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	m.setFocus(focusTree)
	pressAll(t, m, "space", "c")
	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d, want the db3 console reused", len(m.tabs))
	}
	m.setFocus(focusTree)
	pressAll(t, m, "g", "g", "space", "c")
	if c := m.activeTab().(*redisConsoleTab); c.Title() != "redis:cache/db2" || len(m.tabs) != 2 {
		t.Fatalf("connection node console = %q, tabs %d", c.Title(), len(m.tabs))
	}
	if c.Context() != keymap.Console {
		t.Fatalf("context = %v", c.Context())
	}
	c.Handle(keymap.FocusResults)
	if c.Context() != keymap.RedisResults {
		t.Fatalf("results context = %v", c.Context())
	}
	if conn, ref := c.Target(); conn != "cache" || ref.Database != "3" {
		t.Fatalf("Target = %q %+v", conn, ref)
	}
}

func TestRedisConsoleRunLine(t *testing.T) {
	m, b, r := newRedisModel(t, config.Connection{})
	r.put(0, "a", fakeValue{typ: "string", str: "1"})
	c := openRConsole(t, m, 0)
	c.editor.SetValue("SET a 2\nGET a")
	c.editor.SetCursor(1, 0)
	pressAll(t, m, "enter")
	if got := sent(b); !slices.Equal(got, []string{"GET a"}) {
		t.Fatalf("sent %q", got)
	}
	if !slices.Equal(c.text, []string{"1"}) {
		t.Fatalf("text = %q", c.text)
	}
}

func TestRedisConsoleRunAll(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	c.editor.SetValue("SET a 1\n\n# note\nGET a")
	pressAll(t, m, "R")
	if got := sent(b); !slices.Equal(got, []string{"SET a 1", "GET a"}) {
		t.Fatalf("sent %q", got)
	}
	ok := 0
	for _, l := range c.messages {
		if strings.HasPrefix(l, "✓ ") {
			ok++
		}
	}
	if ok != 2 || !strings.Contains(c.messages[0], "SET a 1 — OK in") {
		t.Fatalf("messages %q", c.messages)
	}
	if !strings.HasPrefix(c.status, "2 command(s) ok in") {
		t.Fatalf("status %q", c.status)
	}
}

func TestRedisConsoleRunFromCursor(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	c.editor.SetValue("SET a 1\nSET b 2\nGET b")
	c.editor.SetCursor(1, 0)
	drainAll(t, m, c.Handle(keymap.RunFromCursor))
	if got := sent(b); !slices.Equal(got, []string{"SET b 2", "GET b"}) {
		t.Fatalf("sent %q", got)
	}
}

func TestRedisConsoleSelection(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	c.editor.SetValue("SET a 1\nSET b 2\nGET b\nGET a")
	c.editor.SetCursor(1, 0)
	pressAll(t, m, "V", "j", "enter")
	if got := sent(b); !slices.Equal(got, []string{"SET b 2", "GET b"}) {
		t.Fatalf("sent %q", got)
	}
	if c.editor.Visual() {
		t.Fatal("VISUAL kept after running the selection")
	}
}

func TestRedisConsoleStopsOnError(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "SET a 1\nLPUSH a x\nGET a", keymap.RunAll)
	if got := sent(b); !slices.Equal(got, []string{"SET a 1", "LPUSH a x"}) {
		t.Fatalf("sent %q", got)
	}
	if !strings.HasPrefix(c.status, "command 2/3 failed: WRONGTYPE") || !c.statusErr {
		t.Fatalf("status %q", c.status)
	}
	if line, col := c.editor.Cursor(); line != 1 || col != 0 || c.onResults {
		t.Fatalf("cursor %d:%d, onResults %v", line, col, c.onResults)
	}
	runR(t, m, c, "LPUSH a x", keymap.RunAll)
	if !strings.HasPrefix(c.status, "WRONGTYPE") {
		t.Fatalf("single status %q", c.status)
	}
}

func TestRedisConsoleSplitError(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, `GET a`+"\n"+`SET "a`, keymap.RunAll)
	if !strings.Contains(c.status, "line 2, col 5: unterminated quote") || len(sent(b)) != 0 {
		t.Fatalf("status %q, sent %q", c.status, sent(b))
	}
}

func TestRedisConsoleNothingToRun(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "# only a comment", keymap.RunStatement)
	if c.status != "nothing to run" {
		t.Fatalf("status %q", c.status)
	}
}

func TestRedisConsoleTable(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.put(0, "h", fakeValue{typ: "hash", hash: [][2]string{{"f1", "v1"}, {"f2", "v2"}}})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "HGETALL h", keymap.RunStatement)
	if n := c.grid.RowCount(); n != 2 {
		t.Fatalf("rows = %d", n)
	}
	if got := c.resultsTitle(); got != "Result · 2 rows" {
		t.Fatalf("title %q", got)
	}
	if !strings.HasPrefix(c.status, "1 command(s) ok in") {
		t.Fatalf("status %q", c.status)
	}
	if g, ok := m.activeTab().(gridder); !ok || g.activeGrid() != c.grid {
		t.Fatal("the grid is not the active grid")
	}
	if src, ok := m.source(); !ok || len(src.rs.Rows) != 2 {
		t.Fatalf("export source = %+v %v", src, ok)
	}
	drainAll(t, m, c.Handle(keymap.ToggleMessages))
	if c.resultsTitle() != "Messages" {
		t.Fatalf("title %q", c.resultsTitle())
	}
}

func TestRedisConsoleTruncated(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.fill(0, "k", 30)
	c := openRConsole(t, m, 0)
	runR(t, m, c, "SCAN 0", keymap.RunStatement)
	if c.grid.RowCount() != m.settings.MaxRows || !strings.Contains(c.resultsTitle(), "truncated") {
		t.Fatalf("rows %d, title %q", c.grid.RowCount(), c.resultsTitle())
	}
}

func TestRedisConsoleSelectTitle(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "SELECT 2", keymap.RunStatement)
	if c.Title() != "redis:cache/db2" {
		t.Fatalf("title %q", c.Title())
	}
	if _, ref := c.Target(); ref.Database != "2" {
		t.Fatalf("target %+v", ref)
	}
}

func TestRedisConsoleCancel(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	c.editor.SetValue("BLPOP q 0")
	cmds := c.Handle(keymap.RunStatement)().(tea.BatchMsg)
	done := make(chan tea.Msg, 1)
	go func() { done <- cmds[0]() }()
	if cmd := c.Handle(keymap.RunStatement); cmd != nil || !strings.Contains(c.status, "already running") {
		t.Fatalf("second run: status %q", c.status)
	}
	pressAll(t, m, "ctrl+c")
	if c.status != "canceling…" {
		t.Fatalf("status %q", c.status)
	}
	select {
	case msg := <-done:
		drainAll(t, m, func() tea.Msg { return msg })
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop the command")
	}
	if c.running || !strings.Contains(c.status, "canceled") {
		t.Fatalf("running %v, status %q", c.running, c.status)
	}
	if !strings.Contains(strings.Join(c.messages, "\n"), "BLPOP q 0 — canceled") {
		t.Fatalf("messages %q", c.messages)
	}
}

func TestRedisConsoleDraft(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	openRConsole(t, m, 0)
	pressAll(t, m, "i")
	typeText(t, m, "GET a")
	st := m.store.(*fakeStore)
	if got := st.draft(rconsoleTabKey("cache", 0)); got != "GET a" {
		t.Fatalf("draft = %q", got)
	}
	drainAll(t, m, m.closeTab(m.active))
	if err := st.SaveDraft(rconsoleTabKey("cache", 0), "GET saved"); err != nil {
		t.Fatal(err)
	}
	c := openRConsole(t, m, 0)
	if got := c.editor.Value(); got != "GET saved" {
		t.Fatalf("reopened text = %q", got)
	}
}

func TestRedisConsoleHistory(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "AUTH secret\nGET a", keymap.RunAll)
	st := m.store.(*fakeStore)
	st.mu.Lock()
	entries := slices.Clone(st.entries)
	st.mu.Unlock()
	if len(entries) != 2 || entries[0].SQL != "AUTH ***" || entries[1].SQL != "GET a" || entries[1].Database != "0" || entries[1].Conn != "cache" {
		t.Fatalf("history = %+v", entries)
	}
	if strings.Contains(strings.Join(c.messages, "\n"), "secret") {
		t.Fatalf("messages show the password: %q", c.messages)
	}

	drainAll(t, m, m.useHistory(history.Entry{Conn: "cache", Database: "0", SQL: "DBSIZE"}))
	if got := c.editor.Value(); got != "AUTH secret\nGET a\nDBSIZE" {
		t.Fatalf("history insert = %q", got)
	}
	m.setFocus(focusTree)
	drainAll(t, m, m.useHistory(history.Entry{Conn: "cache", Database: "5", SQL: "GET b"}))
	if c5, ok := m.activeTab().(*redisConsoleTab); !ok || c5.Title() != "redis:cache/db5" || c5.editor.Value() != "GET b" {
		t.Fatalf("history console = %T", m.activeTab())
	}
}

func TestRedisConsoleRecent(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	drainAll(t, m, m.openRecent(history.Recent{Kind: history.RecentConsole, Conn: "cache", Database: "4"}))
	c, ok := m.activeTab().(*redisConsoleTab)
	if !ok || c.Title() != "redis:cache/db4" {
		t.Fatalf("tab = %T", m.activeTab())
	}
	st := m.store.(*fakeStore)
	st.mu.Lock()
	last := st.recents[len(st.recents)-1]
	st.mu.Unlock()
	if last.Kind != history.RecentConsole || last.Conn != "cache" || last.Database != "4" {
		t.Fatalf("recent = %+v", last)
	}
}

func TestRedisConsolePassword(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	b.needPassword = true
	runR(t, m, c, "SET a 1", keymap.RunStatement)
	if m.overlay == nil {
		t.Fatal("no password prompt")
	}
	b.needPassword = false
	typeText(t, m, "pw")
	pressAll(t, m, "enter")
	if got := sent(b); !slices.Equal(got, []string{"SET a 1"}) {
		t.Fatalf("sent %q", got)
	}
}

func TestRedisConsoleToggleTx(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	openRConsole(t, m, 0)
	pressAll(t, m, "space", "t")
	if m.status != "redis transactions: use MULTI … EXEC / DISCARD" {
		t.Fatalf("status %q", m.status)
	}
	command(t, m, "commit")
	if m.status != "redis transactions: use MULTI … EXEC / DISCARD" {
		t.Fatalf(":commit status %q", m.status)
	}
	command(t, m, "w x.redis")
	if m.status != "not supported for redis yet" {
		t.Fatalf(":w status %q", m.status)
	}
	command(t, m, "e x.redis")
	if m.status != "not supported for redis yet" {
		t.Fatalf(":e status %q", m.status)
	}
}

func TestRedisConsoleProdFrame(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{Env: config.EnvProd})
	openRConsole(t, m, 0)
	if s := statusLine(m); !strings.Contains(s, "[PROD]") {
		t.Fatalf("status: %s", s)
	}
}

func TestRedisConsoleScalarText(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	c.editor.SetValue("INCR n")
	drainAll(t, m, c.Handle(keymap.RunStatement))
	if !slices.Equal(c.text, []string{"(integer) 1"}) || c.showMessages {
		t.Fatalf("text %q, messages %v", c.text, c.showMessages)
	}
	s := b.lastSession()
	s.setAnswer(func(args []string) (rdb.Reply, bool) {
		return rdb.Reply{Kind: rdb.KindDouble, Float: 2.5}, args[0] == "ZSCORE"
	})
	runR(t, m, c, "ZSCORE z m", keymap.RunStatement)
	if !slices.Equal(c.text, []string{"(double) 2.5"}) {
		t.Fatalf("text %q", c.text)
	}
}

func boolPtr(b bool) *bool { return &b }

func TestRedisConsoleReadOnly(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{ReadOnly: boolPtr(true)})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "GET a\nSET a 1", keymap.RunAll)
	if !strings.Contains(c.status, ":set noreadonly") || len(sent(b)) != 0 {
		t.Fatalf("status %q, sent %q", c.status, sent(b))
	}
	if line, _ := c.editor.Cursor(); line != 1 {
		t.Fatalf("cursor on line %d, want the refused one", line)
	}
	if !strings.Contains(strings.Join(c.messages, "\n"), "✗ SET a 1 — ") {
		t.Fatalf("messages %q", c.messages)
	}
}

func TestRedisConsoleMultiBypass(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{ReadOnly: boolPtr(true)})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "MULTI\nSET a 1\nEXEC", keymap.RunAll)
	if len(sent(b)) != 0 {
		t.Fatalf("batch sent %q", sent(b))
	}
	for _, line := range []string{"MULTI", "SET a 1", "DISCARD"} {
		runR(t, m, c, line, keymap.RunStatement)
	}
	if got := sent(b); !slices.Equal(got, []string{"MULTI", "DISCARD"}) {
		t.Fatalf("sent %q", got)
	}
}

func TestRedisConsoleProdConfirm(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{Env: config.EnvProd, ReadOnly: boolPtr(false)})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "SET a 1", keymap.RunStatement)
	if m.overlay == nil || !strings.Contains(screen(m), "Write to PROD") {
		t.Fatalf("no confirmation:\n%s", screen(m))
	}
	pressAll(t, m, "esc")
	if len(sent(b)) != 0 {
		t.Fatalf("sent %q after esc", sent(b))
	}
	runR(t, m, c, "SET a 1", keymap.RunStatement)
	pressAll(t, m, "y")
	if got := sent(b); !slices.Equal(got, []string{"SET a 1"}) {
		t.Fatalf("sent %q", got)
	}
	runR(t, m, c, "GET a", keymap.RunStatement)
	if m.overlay != nil {
		t.Fatal("a read asks for confirmation")
	}
}

func TestRedisConsoleDangerProd(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{Env: config.EnvProd, ReadOnly: boolPtr(false)})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "FLUSHDB", keymap.RunStatement)
	if !strings.Contains(screen(m), "PROD · Dangerous command") {
		t.Fatalf("no confirmation:\n%s", screen(m))
	}
	pressAll(t, m, "y", "enter")
	if len(sent(b)) != 0 {
		t.Fatal("y alone ran FLUSHDB on prod")
	}
	pressAll(t, m, "backspace")
	typeText(t, m, "cache")
	pressAll(t, m, "enter")
	if got := sent(b); !slices.Equal(got, []string{"FLUSHDB"}) {
		t.Fatalf("sent %q", got)
	}
}

func TestRedisConsoleDangerDev(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "FLUSHDB", keymap.RunStatement)
	if !strings.Contains(screen(m), "Dangerous command") {
		t.Fatalf("no confirmation:\n%s", screen(m))
	}
	pressAll(t, m, "y")
	if got := sent(b); !slices.Equal(got, []string{"FLUSHDB"}) {
		t.Fatalf("sent %q", got)
	}
}

func TestRedisConsoleKeys(t *testing.T) {
	m, b, r := newRedisModel(t, config.Connection{})
	r.fill(0, "k", 20000)
	c := openRConsole(t, m, 0)
	runR(t, m, c, "KEYS *", keymap.RunStatement)
	if !strings.Contains(c.status, "SCAN") || len(sent(b)) != 0 || m.overlay != nil {
		t.Fatalf("status %q, sent %q", c.status, sent(b))
	}
	r.fill(5, "k", 5)
	runR(t, m, c, "SELECT 5", keymap.RunStatement)
	runR(t, m, c, "KEYS *", keymap.RunStatement)
	if !strings.Contains(screen(m), "Dangerous command") {
		t.Fatalf("no confirmation on a small db:\n%s", screen(m))
	}
	pressAll(t, m, "y")
	if got := sent(b); !slices.Equal(got, []string{"SELECT 5", "KEYS *"}) {
		t.Fatalf("sent %q", got)
	}
}

func TestRedisConsoleUnsupported(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "SUBSCRIBE ch", keymap.RunStatement)
	if c.status != "SUBSCRIBE is not supported in datavim" || len(sent(b)) != 0 {
		t.Fatalf("status %q, sent %q", c.status, sent(b))
	}
}

func TestRedisConsoleNoReadonlyUnlock(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{ReadOnly: boolPtr(true)})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "SET a 1", keymap.RunStatement)
	command(t, m, "set noreadonly")
	runR(t, m, c, "SET a 1", keymap.RunStatement)
	if got := sent(b); !slices.Equal(got, []string{"SET a 1"}) {
		t.Fatalf("sent %q", got)
	}
}

func TestRedisConsoleStaleConfirm(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "FLUSHDB", keymap.RunStatement)
	ok := redisConfirmMsg{tabID: c.id, gen: c.gen}
	pressAll(t, m, "esc")
	runR(t, m, c, "GET a", keymap.RunStatement)
	drainAll(t, m, func() tea.Msg { return ok })
	if got := sent(b); slices.Contains(got, "FLUSHDB") {
		t.Fatalf("a stale confirmation ran: %q", got)
	}
}

func TestRedisConsoleTxBadge(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "MULTI\nSET a 1", keymap.RunAll)
	if s := statusLine(m); !strings.Contains(s, "TX 1") {
		t.Fatalf("status bar: %s", s)
	}
	if !strings.Contains(strings.Join(c.messages, "\n"), "SET a 1 — QUEUED") {
		t.Fatalf("messages %q", c.messages)
	}
	runR(t, m, c, "EXEC", keymap.RunStatement)
	if s := statusLine(m); strings.Contains(s, "TX") || c.TxStatus() != "" {
		t.Fatalf("status bar after EXEC: %s", s)
	}
}

func TestRedisConsoleNoPingInMulti(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "MULTI", keymap.RunStatement)
	pings := b.lastSession().pingCount()
	runR(t, m, c, "SET a 1", keymap.RunStatement)
	runR(t, m, c, "EXEC", keymap.RunStatement)
	if got := b.lastSession().pingCount(); got != pings {
		t.Fatalf("pings %d → %d inside MULTI", pings, got)
	}
	runR(t, m, c, "GET a", keymap.RunStatement)
	if got := b.lastSession().pingCount(); got != pings+1 {
		t.Fatalf("no ping after EXEC: %d", got)
	}
}

func TestRedisConsoleCloseInTx(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "MULTI", keymap.RunStatement)
	command(t, m, "q")
	if !strings.Contains(screen(m), "Open transaction") || len(m.tabs) != 1 {
		t.Fatalf(":q in MULTI:\n%s", screen(m))
	}
	pressAll(t, m, "esc")
	command(t, m, "qa")
	if s := screen(m); !strings.Contains(s, "Open transactions") || !strings.Contains(s, "redis:cache/db0") {
		t.Fatalf(":qa in MULTI:\n%s", s)
	}
	pressAll(t, m, "esc")
	m.setFocus(focusTree)
	pressAll(t, m, "g", "g")
	if m.canChange("cache") {
		t.Fatal("the connection can be changed under an open MULTI")
	}
}

func TestRedisConsoleReconnectRestoresDB(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "SELECT 3", keymap.RunStatement)
	c.editor.SetValue("BLPOP q 0")
	cmds := c.Handle(keymap.RunStatement)().(tea.BatchMsg)
	done := make(chan tea.Msg, 1)
	go func() { done <- cmds[0]() }()
	time.Sleep(10 * time.Millisecond)
	c.Handle(keymap.Cancel)
	drainAll(t, m, func() tea.Msg { return <-done })
	if c.Title() != "redis:cache/db3" {
		t.Fatalf("title after cancel %q", c.Title())
	}
	runR(t, m, c, "GET a", keymap.RunStatement)
	if got := b.sessionCalls(); len(got) != 2 || got[1] != 3 {
		t.Fatalf("RedisSession calls %v", got)
	}
	if !strings.Contains(strings.Join(c.messages, "\n"), "reconnected (db 3)") || c.Title() != "redis:cache/db3" {
		t.Fatalf("messages %q, title %q", c.messages, c.Title())
	}
}

func TestRedisConsoleTxLost(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "MULTI", keymap.RunStatement)
	b.lastSession().setLost()
	runR(t, m, c, "SET a 1", keymap.RunStatement)
	if !strings.Contains(strings.Join(c.messages, "\n"), "transaction lost") || c.TxStatus() != "" {
		t.Fatalf("messages %q, tx %q", c.messages, c.TxStatus())
	}
	if got := b.sessionCalls(); len(got) != 1 {
		t.Fatalf("reconnected inside the lost transaction: %v", got)
	}
	runR(t, m, c, "GET a", keymap.RunStatement)
	if got := b.sessionCalls(); len(got) != 2 {
		t.Fatalf("the next run did not reconnect: %v", got)
	}
}

func TestRedisConsoleWatchPingLost(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "WATCH k", keymap.RunStatement)
	if c.TxStatus() != "WATCH" {
		t.Fatalf("tx %q", c.TxStatus())
	}
	s := b.lastSession()
	s.setLost()
	runR(t, m, c, "GET a", keymap.RunStatement)
	if !strings.Contains(strings.Join(c.messages, "\n"), "transaction lost") || c.TxStatus() != "" {
		t.Fatalf("messages %q", c.messages)
	}
	if slices.Contains(s.commands(), "GET a") || len(b.sessionCalls()) != 1 {
		t.Fatalf("sent %q, sessions %v", s.commands(), b.sessionCalls())
	}
}

func TestRedisConsoleSetReadonlyInTx(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{ReadOnly: boolPtr(false)})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "MULTI", keymap.RunStatement)
	command(t, m, "set readonly")
	if !strings.Contains(m.status, "open transaction") {
		t.Fatalf("status %q", m.status)
	}
	if cfg, _ := b.Connection("cache"); cfg.IsReadOnly() {
		t.Fatal("read-only changed under an open MULTI")
	}
}

func TestRedisConsoleOpenCellKey(t *testing.T) {
	m, b, r := newRedisModel(t, config.Connection{})
	r.put(0, "\xff", fakeValue{typ: "string", str: "v"})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "PING", keymap.RunStatement)
	b.lastSession().setAnswer(func(args []string) (rdb.Reply, bool) {
		return rarr(rs("0"), rarr(rs("\xff"), rs("b"))), args[0] == "SCAN"
	})
	runR(t, m, c, "SCAN 0", keymap.RunStatement)
	pressAll(t, m, "ctrl+j", "l", "g", "d")
	k, ok := m.activeTab().(*keyTab)
	if !ok || k.Title() != `\xFF` {
		t.Fatalf("tab = %T, status %q", m.activeTab(), m.status)
	}
	if calls := r.keyCalls(); !slices.Contains(calls, "KeyInfo \xff") {
		t.Fatalf("key calls %q", calls)
	}
}

func TestRedisConsoleOpenCellKeyDB(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.put(2, "k2", fakeValue{typ: "string", str: "v"})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "SELECT 2\nKEYS *", keymap.RunAll)
	pressAll(t, m, "y") // KEYS asks first
	pressAll(t, m, "ctrl+j", "l", "g", "d")
	k, ok := m.activeTab().(*keyTab)
	if !ok || k.db != 2 || k.key != "k2" {
		t.Fatalf("tab = %T %+v, status %q", m.activeTab(), m.activeTab(), m.status)
	}
}

func TestRedisConsoleOpenCellKeyText(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "GET a", keymap.RunStatement)
	pressAll(t, m, "ctrl+j", "g", "d")
	if c.status != "no key here: the result is not a table" {
		t.Fatalf("status %q", c.status)
	}
}

func TestRedisConsoleOpenCellKeyMissing(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	r.put(0, "gone", fakeValue{typ: "string", str: "v"})
	runR(t, m, c, "SCAN 0", keymap.RunStatement)
	r.del(0, "gone")
	pressAll(t, m, "ctrl+j", "l", "g", "d")
	if !strings.Contains(m.status, "key not found: gone") {
		t.Fatalf("status %q", m.status)
	}
}

func TestRedisLateRunResultForClosedTabClosesSession(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	s := &fakeRedisSession{r: r}
	_, cmd := m.Update(redisRunMsg{tabID: 99, session: s})
	drainAll(t, m, cmd)
	if !s.closed {
		t.Fatal("the session of a closed console was left open")
	}
}
