package ui

import (
	"errors"
	"fmt"
	"io"
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
	c := newConsoleTab(1, b, conn, db.Namespace{Database: "app"}, config.Settings{MaxRows: 2, MaxColumnWidth: 40}, timings{})
	c.SetSize(80, 24)
	return c
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
