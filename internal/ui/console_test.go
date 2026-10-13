package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

func newConsole(t *testing.T, b *fakeBackend, conn string) *consoleTab {
	t.Helper()
	c, _ := newConsoleWithStore(t, b, conn)
	return c
}

func newConsoleWithStore(t *testing.T, b *fakeBackend, conn string) (*consoleTab, *fakeStore) {
	t.Helper()
	st := newFakeStore()
	c := newConsoleTab(1, b, st, conn, db.Namespace{Database: "app"}, config.Settings{MaxRows: 2, MaxColumnWidth: 40}, timings{})
	c.SetSize(80, 24)
	return c, st
}

// runTab runs cmd, feeding messages addressed to tab back into it, and
// returns every other message (e.g. overlays).
func runTab(t *testing.T, tab tab, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	var other []tea.Msg
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 100 {
			t.Fatal("runTab: too many steps")
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
		case tabMsg:
			if msg.TabID() == tab.ID() {
				queue = append(queue, tab.Update(msg))
			} else {
				other = append(other, msg)
			}
		default:
			other = append(other, msg)
		}
	}
	return other
}

func consoleView(c *consoleTab) string { return ansi.Strip(c.View(true)) }

func TestConsoleRunsStatementUnderCursor(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.editor.SetValue("select 1;\nselect 2;")
	c.editor.Top()
	runTab(t, c, c.Handle(keymap.RunStatement))
	if got := b.conn.lastQuery(); got != "select 1" {
		t.Fatalf("ran %q", got)
	}
	if got := b.conn.execList(); !slices.Equal(got, []string{"USE `app`"}) {
		t.Fatalf("session scope: %v", got)
	}
	v := consoleView(c)
	for _, want := range []string{"user1", "user2", "2 rows", "truncated"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "user3") {
		t.Error("max_rows caps the result")
	}
	c.Handle(keymap.ToggleMessages)
	if v := consoleView(c); !strings.Contains(v, "✓ select 1") {
		t.Fatalf("messages:\n%s", v)
	}
}

func TestConsoleRunAllShowsLastResultSet(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.editor.SetValue("select 1; update t set a = 1 where id = 1")
	runTab(t, c, c.Handle(keymap.RunAll))
	if got := b.conn.execList(); !slices.Contains(got, "update t set a = 1 where id = 1") {
		t.Fatalf("execs = %v", got)
	}
	if !strings.Contains(consoleView(c), "user1") {
		t.Fatalf("the select result stays visible:\n%s", consoleView(c))
	}
	c.Handle(keymap.ToggleMessages)
	if v := consoleView(c); !strings.Contains(v, "1 rows affected") {
		t.Fatalf("messages:\n%s", v)
	}
}

func TestConsoleOnlyExecShowsMessages(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.editor.SetValue("set @a = 1")
	runTab(t, c, c.Handle(keymap.RunStatement))
	if v := consoleView(c); !strings.Contains(v, "Messages") || !strings.Contains(v, "set @a = 1") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestConsoleReadOnlyRejectsWrites(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "prod") // env = prod -> read-only
	c.editor.SetValue("delete from t where id = 1")
	if cmd := c.Handle(keymap.RunStatement); cmd != nil {
		t.Fatal("nothing runs")
	}
	if len(b.conn.execList()) != 0 {
		t.Fatal("no statement reached the server")
	}
	if v := consoleView(c); !strings.Contains(v, "read-only") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestConsoleMissingWhereNeedsTableName(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.editor.SetValue("update t set a = 1")
	other := runTab(t, c, c.Handle(keymap.RunStatement))
	if len(other) != 1 {
		t.Fatalf("messages = %#v", other)
	}
	o := other[0].(openOverlayMsg).overlay
	if !strings.Contains(ansi.Strip(o.View(80, 20)), "no WHERE clause") {
		t.Fatalf("confirm:\n%s", ansi.Strip(o.View(80, 20)))
	}
	if len(b.conn.execList()) != 0 {
		t.Fatal("nothing runs before confirming")
	}
	o, _ = o.HandleKey(uitest.Key("t"))
	_, cmd := o.HandleKey(uitest.Key("enter"))
	runTab(t, c, cmd)
	if got := b.conn.execList(); !slices.Contains(got, "update t set a = 1") {
		t.Fatalf("execs = %v", got)
	}
}

func TestConsoleShowsErrors(t *testing.T) {
	b := newFakeBackend()
	b.conn.setFail("select bad", errors.New("syntax error near bad"))
	c := newConsole(t, b, "local")
	c.editor.SetValue("select bad")
	runTab(t, c, c.Handle(keymap.RunStatement))
	if v := consoleView(c); !strings.Contains(v, "syntax error near bad") || !strings.Contains(v, "Messages") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestConsoleCancel(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.editor.SetValue(slowQuery)
	cmds := c.Handle(keymap.RunStatement)().(tea.BatchMsg)
	done := make(chan tea.Msg, 1)
	go func() { done <- cmds[0]() }()
	if v := consoleView(c); !strings.Contains(v, "ctrl+c cancel") {
		t.Fatalf("running view:\n%s", v)
	}
	if cmd := c.Handle(keymap.RunStatement); cmd != nil {
		t.Fatal("only one query runs at a time")
	}
	c.Handle(keymap.Cancel)
	select {
	case msg := <-done:
		c.Update(msg)
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop the query")
	}
	if c.running || !strings.Contains(consoleView(c), "canceled") {
		t.Fatalf("view:\n%s", consoleView(c))
	}
	if c.session == nil {
		t.Fatal("the session survives a cancel")
	}
}

func TestConsoleReusesAndReopensSession(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.editor.SetValue("select 1")
	runTab(t, c, c.Handle(keymap.RunStatement))
	runTab(t, c, c.Handle(keymap.RunStatement))
	if n := b.conn.sessionCount(); n != 1 {
		t.Fatalf("sessions = %d, want 1", n)
	}
	b.conn.mu.Lock()
	b.conn.pingErr = fmt.Errorf("read: %w", io.EOF)
	b.conn.mu.Unlock()
	runTab(t, c, c.Handle(keymap.RunStatement))
	if n := b.conn.sessionCount(); n != 2 {
		t.Fatalf("sessions = %d, want a new one after a failed ping", n)
	}
	c.Handle(keymap.ToggleMessages)
	if !strings.Contains(consoleView(c), "reconnected") {
		t.Fatalf("view:\n%s", consoleView(c))
	}
}

func TestConsoleDropsSessionOnConnectionLoss(t *testing.T) {
	b := newFakeBackend()
	b.conn.setFail("select 1", fmt.Errorf("%w: broken pipe", db.ErrConnLost))
	c := newConsole(t, b, "local")
	c.editor.SetValue("select 1")
	runTab(t, c, c.Handle(keymap.RunStatement))
	if c.session != nil {
		t.Fatal("a dead session is dropped")
	}
	if !strings.Contains(consoleView(c), "connection lost") {
		t.Fatalf("view:\n%s", consoleView(c))
	}
	if n := len(b.conn.queries); n != 1 {
		t.Fatalf("statements are never retried automatically: %d queries", n)
	}
}

func TestConsoleLoadMore(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.editor.SetValue("select * from users")
	runTab(t, c, c.Handle(keymap.RunStatement))
	c.Handle(keymap.FocusResults)
	if c.Context() != keymap.Results {
		t.Fatal("ctrl+j moves to the results")
	}
	runTab(t, c, c.Handle(keymap.LoadMore))
	v := consoleView(c)
	if !strings.Contains(v, "user3") || strings.Contains(v, "truncated") {
		t.Fatalf("view:\n%s", v)
	}
	if cmd := c.Handle(keymap.LoadMore); cmd != nil {
		t.Fatal("nothing more to load")
	}
}

func TestConsoleInsertMode(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	if c.CapturesInput() || c.Mode() != "NORMAL" {
		t.Fatal("starts in NORMAL")
	}
	c.Handle(keymap.InsertMode)
	if !c.CapturesInput() || c.Mode() != "INSERT" {
		t.Fatal("i enters INSERT")
	}
	for _, k := range uitest.Type("select 1") {
		c.Input(k)
	}
	runTab(t, c, c.Input(uitest.Key("ctrl+j")))
	if b.conn.lastQuery() != "select 1" {
		t.Fatalf("ctrl+j runs in INSERT mode, ran %q", b.conn.lastQuery())
	}
	c.Input(uitest.Key("esc"))
	if c.CapturesInput() {
		t.Fatal("esc leaves INSERT")
	}
}

func TestConsoleNothingToRunAndTinySizes(t *testing.T) {
	c := newConsole(t, newFakeBackend(), "local")
	if c.Handle(keymap.RunStatement) != nil || !strings.Contains(consoleView(c), "nothing to run") {
		t.Fatal("empty editor")
	}
	for _, s := range [][2]int{{0, 0}, {1, 1}, {10, 3}, {30, 5}} {
		c.SetSize(s[0], s[1])
		_ = c.View(true)
	}
}

func TestDataTabScrollAndFind(t *testing.T) {
	tab := loadedDataTab(t, newFakeBackend())
	if !tab.Find("USER2", true) {
		t.Fatal("find")
	}
	if _, v, _ := tab.grid.Cell(); v.Text != "user2" {
		t.Fatalf("at %q", v.Text)
	}
	tab.Handle(keymap.ScrollRight) // must not panic
}

func TestConsoleRecordsHistory(t *testing.T) {
	b := newFakeBackend()
	b.conn.setFail("select bad", errors.New("syntax error"))
	c, st := newConsoleWithStore(t, b, "local")
	c.editor.SetValue("select 1; select bad; select 3")
	runTab(t, c, c.Handle(keymap.RunAll))
	if len(st.entries) != 2 {
		t.Fatalf("entries = %+v (stops at the error)", st.entries)
	}
	ok, bad := st.entries[0], st.entries[1]
	if ok.SQL != "select 1" || ok.Conn != "local" || ok.Database != "app" || ok.Err != "" || ok.Time.IsZero() {
		t.Fatalf("ok entry = %+v", ok)
	}
	if bad.SQL != "select bad" || bad.Err != "syntax error" {
		t.Fatalf("bad entry = %+v", bad)
	}
}

func TestConsoleRejectedStatementsAreNotRecorded(t *testing.T) {
	c, st := newConsoleWithStore(t, newFakeBackend(), "prod")
	c.editor.SetValue("delete from t where id = 1")
	runTab(t, c, c.Handle(keymap.RunStatement))
	if len(st.entries) != 0 {
		t.Fatalf("entries = %+v", st.entries)
	}
}

func TestConsoleDrafts(t *testing.T) {
	b := newFakeBackend()
	c, st := newConsoleWithStore(t, b, "local")
	st.drafts[c.Key()] = "select draft"
	runTab(t, c, c.Init())
	if c.editor.Value() != "select draft" {
		t.Fatalf("draft not restored: %q", c.editor.Value())
	}
	c.Handle(keymap.InsertMode)
	c.editor.Bottom()
	runTab(t, c, c.Input(uitest.Key("x")))
	if got := st.draft(c.Key()); got != "select draftx" {
		t.Fatalf("draft after typing = %q", got)
	}
	c.editor.SetValue("select final")
	c.shutdown()()
	if got := st.draft(c.Key()); got != "select final" {
		t.Fatalf("draft on close = %q", got)
	}
}

func TestDraftMergesWithTextTypedBeforeItLoaded(t *testing.T) {
	c, st := newConsoleWithStore(t, newFakeBackend(), "local")
	st.drafts[c.Key()] = "select old"
	c.editor.SetValue("select new")
	runTab(t, c, c.Init())
	if got := c.editor.Value(); got != "select old\nselect new" {
		t.Fatalf("value = %q", got)
	}
}

func TestExternalEdit(t *testing.T) {
	c := newConsole(t, newFakeBackend(), "local")
	c.editor.SetValue("select 1")
	file, ok := run(c.Handle(keymap.ExternalEdit)).(editFileMsg)
	if !ok || file.err != nil {
		t.Fatalf("msg = %#v", file)
	}
	if data, _ := os.ReadFile(file.path); string(data) != "select 1" {
		t.Fatalf("temp file = %q", data)
	}
	if !strings.HasSuffix(file.path, ".sql") {
		t.Fatalf("path = %s", file.path)
	}
	if cmd := c.Update(file); cmd == nil {
		t.Fatal("the editor process is started")
	}
	if err := os.WriteFile(file.path, []byte("select 2"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTab(t, c, c.Update(editDoneMsg{tabID: c.id, path: file.path}))
	if c.editor.Value() != "select 2" {
		t.Fatalf("value = %q", c.editor.Value())
	}
	if _, err := os.Stat(file.path); !os.IsNotExist(err) {
		t.Fatal("temp file removed")
	}

	file2 := run(c.Handle(keymap.ExternalEdit)).(editFileMsg)
	_ = os.WriteFile(file2.path, []byte("garbage"), 0o600)
	runTab(t, c, c.Update(editDoneMsg{tabID: c.id, path: file2.path, err: errors.New("exit status 1")}))
	if c.editor.Value() != "select 2" {
		t.Fatal("a failed editor keeps the text")
	}
	if _, err := os.Stat(file2.path); !os.IsNotExist(err) {
		t.Fatal("temp file removed after a failed edit too")
	}
}

func TestConsoleProdWritesNeedConfirmation(t *testing.T) {
	b := newFakeBackend()
	if err := b.SetReadOnly("prod", false); err != nil {
		t.Fatal(err)
	}
	c := newConsole(t, b, "prod")
	c.editor.SetValue("select 1")
	runTab(t, c, c.Handle(keymap.RunStatement))
	if got := b.conn.lastQuery(); got != "select 1" {
		t.Fatalf("reads run without asking: %q", got)
	}
	c.editor.SetValue("insert into t values (1)")
	other := runTab(t, c, c.Handle(keymap.RunStatement))
	if len(other) != 1 {
		t.Fatalf("messages = %#v", other)
	}
	o := other[0].(openOverlayMsg).overlay
	if v := ansi.Strip(o.View(80, 20)); !strings.Contains(v, "PROD") || !strings.Contains(v, "write t: insert into t values (1)") {
		t.Fatalf("confirm:\n%s", v)
	}
	if slices.Contains(b.conn.execList(), "insert into t values (1)") {
		t.Fatal("nothing runs before confirming")
	}
	_, cmd := o.HandleKey(uitest.Key("y"))
	runTab(t, c, cmd)
	if !slices.Contains(b.conn.execList(), "insert into t values (1)") {
		t.Fatalf("execs = %v", b.conn.execList())
	}
}

func TestConsoleReopensSessionWhenReadOnlyChanges(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.editor.SetValue("select 1")
	runTab(t, c, c.Handle(keymap.RunStatement))
	first := c.session.(*fakeSession)
	if err := b.SetReadOnly("local", true); err != nil {
		t.Fatal(err)
	}
	runTab(t, c, c.Handle(keymap.RunStatement))
	b.conn.mu.Lock()
	closed := first.closed
	b.conn.mu.Unlock()
	if n := b.conn.sessionCount(); n != 2 || !closed {
		t.Fatalf("sessions = %d, first closed = %v", n, closed)
	}
	c.Handle(keymap.ToggleMessages)
	if v := consoleView(c); !strings.Contains(v, "reopened read-only") {
		t.Fatalf("view:\n%s", v)
	}
	runTab(t, c, c.Handle(keymap.RunStatement))
	if n := b.conn.sessionCount(); n != 2 {
		t.Fatalf("sessions = %d: no reopen while the setting is unchanged", n)
	}
}
