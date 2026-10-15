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
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/meta"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
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
	c := newConsoleTab(1, b, st, meta.NewCache(), conn, db.Namespace{Database: "app"}, config.Settings{MaxRows: 2, MaxColumnWidth: 40}, timings{})
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
	c.editor.EnterInsert()
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
	c.editor.EnterInsert()
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

func runSQL(t *testing.T, c *consoleTab, sql string) {
	t.Helper()
	c.editor.SetValue(sql)
	runTab(t, c, c.Handle(keymap.RunStatement))
}

func countOf(list []string, s string) int {
	n := 0
	for _, v := range list {
		if v == s {
			n++
		}
	}
	return n
}

func TestConsoleManualCommit(t *testing.T) {
	b := newFakeBackend()
	c, st := newConsoleWithStore(t, b, "local")
	c.toggleTx()
	if got := c.TxStatus(); got != "TX" {
		t.Fatalf("manual mode, no transaction yet: %q", got)
	}
	runSQL(t, c, "insert into t values (1)")
	runSQL(t, c, "update t set a = 1 where id = 1")
	want := []string{"USE `app`", "START TRANSACTION", "insert into t values (1)", "update t set a = 1 where id = 1"}
	if got := b.conn.execList(); !slices.Equal(got, want) {
		t.Fatalf("execs = %v", got)
	}
	if got := c.TxStatus(); got != "TX 2" {
		t.Fatalf("status = %q", got)
	}
	runTab(t, c, c.endTx(true))
	if got := b.conn.execList(); got[len(got)-1] != "COMMIT" {
		t.Fatalf("execs = %v", got)
	}
	if got := c.TxStatus(); got != "TX" {
		t.Fatalf("after commit: %q", got)
	}
	st.mu.Lock()
	var recorded []string
	for _, e := range st.entries {
		recorded = append(recorded, e.SQL)
	}
	st.mu.Unlock()
	if slices.Contains(recorded, "START TRANSACTION") || !slices.Contains(recorded, "COMMIT") {
		t.Fatalf("history = %v; the injected BEGIN is not recorded", recorded)
	}
	c.toggleTx()
	if got := c.TxStatus(); got != "" {
		t.Fatalf("autocommit: %q", got)
	}
}

func TestConsoleToggleOffNeedsCommit(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.toggleTx()
	runSQL(t, c, "insert into t values (1)")
	c.toggleTx()
	if !c.manualTx || !strings.Contains(consoleView(c), ":commit or :rollback") {
		t.Fatalf("manual=%v view:\n%s", c.manualTx, consoleView(c))
	}
	if !c.openTx() {
		t.Fatal("openTx")
	}
}

func TestConsoleTracksTypedTransactionStatements(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	steps := []struct{ sql, want string }{
		{"begin", "TX 0"},
		{"delete from t where id = 1", "TX 1"},
		{"commit", ""},
		{"start transaction", "TX 0"},
		{"insert into t values (1)", "TX 1"},
		{"commit and chain", "TX 0"},
		{"rollback", ""},
	}
	for _, s := range steps {
		runSQL(t, c, s.sql)
		if got := c.TxStatus(); got != s.want {
			t.Fatalf("after %q: status = %q, want %q", s.sql, got, s.want)
		}
	}
	if n := countOf(b.conn.execList(), "START TRANSACTION"); n != 0 {
		t.Fatal("autocommit mode never injects BEGIN")
	}
}

func TestConsoleMySQLDDLCommitsImplicitly(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.toggleTx()
	runSQL(t, c, "insert into t values (1)")
	runSQL(t, c, "create table x (id int)")
	if got := c.TxStatus(); got != "TX" {
		t.Fatalf("status = %q", got)
	}
	// A statement without a result set already shows the messages pane.
	if !strings.Contains(consoleView(c), "implicitly") {
		t.Fatalf("view:\n%s", consoleView(c))
	}
	runSQL(t, c, "insert into t values (2)")
	if n := countOf(b.conn.execList(), "START TRANSACTION"); n != 2 {
		t.Fatalf("START TRANSACTION ran %d times, want 2", n)
	}
}

func TestConsolePostgresFailedStatementAbortsTx(t *testing.T) {
	b := newFakeBackend()
	b.conns = append(b.conns, config.Connection{Name: "pgdev", Driver: "postgres", Env: config.EnvDev})
	c := newConsole(t, b, "pgdev")
	c.toggleTx()
	runSQL(t, c, "insert into t values (1)")
	b.conn.setFail("insert into t values (2)", errors.New("duplicate key"))
	runSQL(t, c, "insert into t values (2)")
	if got := c.TxStatus(); got != "TX failed · :rollback" {
		t.Fatalf("status = %q", got)
	}
	runSQL(t, c, "rollback to savepoint s1")
	if got := c.TxStatus(); got != "TX 1" {
		t.Fatalf("ROLLBACK TO SAVEPOINT recovers: %q", got)
	}
	runSQL(t, c, "insert into t values (2)")
	before := len(b.conn.execList())
	runTab(t, c, c.endTx(true))
	if len(b.conn.execList()) != before || !strings.Contains(consoleView(c), ":rollback") {
		t.Fatalf("a failed transaction is not committed; view:\n%s", consoleView(c))
	}
	runTab(t, c, c.endTx(false))
	if got := b.conn.execList(); got[len(got)-1] != "ROLLBACK" || c.TxStatus() != "TX" {
		t.Fatalf("execs = %v, status = %q", got, c.TxStatus())
	}
	if got := b.conn.execList(); got[0] != "BEGIN" {
		t.Fatalf("PostgreSQL starts with BEGIN: %v", got)
	}
}

func TestConsoleTransactionLostOnDisconnect(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.toggleTx()
	runSQL(t, c, "insert into t values (1)")
	b.conn.mu.Lock()
	b.conn.pingErr = fmt.Errorf("read: %w", io.EOF)
	b.conn.mu.Unlock()
	runSQL(t, c, "select 1")
	if got := b.conn.lastQuery(); got != "" {
		t.Fatalf("nothing runs once the transaction is lost, ran %q", got)
	}
	if c.session != nil || c.TxStatus() != "TX" {
		t.Fatalf("session = %v, status = %q", c.session, c.TxStatus())
	}
	if v := consoleView(c); !strings.Contains(v, "transaction lost") {
		t.Fatalf("view:\n%s", v)
	}
	b.conn.mu.Lock()
	b.conn.pingErr = nil
	b.conn.mu.Unlock()
	runSQL(t, c, "select 1")
	if b.conn.sessionCount() != 2 || b.conn.lastQuery() != "select 1" {
		t.Fatalf("sessions = %d, last query = %q", b.conn.sessionCount(), b.conn.lastQuery())
	}
	if n := countOf(b.conn.execList(), "START TRANSACTION"); n != 2 {
		t.Fatalf("a new transaction starts on the new session: %d", n)
	}
}

func TestConsoleEndTxWithoutTransaction(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	if cmd := c.endTx(true); cmd != nil {
		t.Fatal("nothing to commit")
	}
	if !strings.Contains(consoleView(c), "no open transaction") {
		t.Fatalf("view:\n%s", consoleView(c))
	}
}

func TestConsoleDDLReportsStaleCatalog(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.editor.SetValue("create table t2 (id int)")
	other := runTab(t, c, c.Handle(keymap.RunStatement))
	if !slices.Contains(other, tea.Msg(metaStaleMsg{conn: "local", database: "app"})) {
		t.Fatalf("messages: %#v", other)
	}
	c.editor.SetValue("select 1")
	if other := runTab(t, c, c.Handle(keymap.RunStatement)); slices.ContainsFunc(other, func(m tea.Msg) bool { _, ok := m.(metaStaleMsg); return ok }) {
		t.Fatal("queries do not change the catalog")
	}
}

func TestConsoleJumpsToErrorPosition(t *testing.T) {
	b := newFakeBackend()
	q := "SELECT '李' FORM t"
	b.conn.setFail(q, &db.PositionError{Err: errors.New(`syntax error at or near "FORM"`), Offset: strings.Index(q, "FORM")})
	c := newConsole(t, b, "local")
	text := "select 1;\n" + q + ";"
	c.editor.SetValue(text) // the cursor is in the second statement
	cmd := c.Handle(keymap.RunStatement)
	c.onResults = true // e.g. the user looked at the previous results meanwhile
	runTab(t, c, cmd)
	if got, want := c.editor.Offset(), strings.Index(text, "FORM"); got != want {
		t.Fatalf("cursor at %d, want %d", got, want)
	}
	if !strings.Contains(c.status, "line 2, col 12") || !strings.Contains(c.status, "FORM") {
		t.Fatalf("status = %q", c.status)
	}
	if c.onResults {
		t.Error("the editor gets the focus")
	}
}

func TestConsoleDoesNotJumpWhenTextChanged(t *testing.T) {
	b := newFakeBackend()
	q := "SELECT * FORM t"
	b.conn.setFail(q, &db.PositionError{Err: errors.New("syntax error"), Offset: 9})
	c := newConsole(t, b, "local")
	c.editor.SetValue(q)
	cmd := c.Handle(keymap.RunStatement)
	c.editor.SetValue("something else")
	runTab(t, c, cmd)
	if strings.Contains(c.status, "line ") || c.editor.Offset() != len("something else")-1 {
		t.Fatalf("status %q, cursor %d", c.status, c.editor.Offset())
	}
}

func TestConsoleAsksForThePasswordAndRunsAgain(t *testing.T) {
	m, b := newTestModel(t)
	b.needPassword = true
	press(t, m, "space", "c") // a console on local, never connected
	c := m.activeTab().(*consoleTab)
	c.editor.SetValue("select 42")
	press(t, m, "enter")
	if !strings.Contains(screen(m), "Password for local") {
		t.Fatalf("prompt not shown:\n%s", screen(m))
	}
	press(t, m, "s", "e", "c", "enter")
	if q := b.conn.lastQuery(); q != "select 42" {
		t.Fatalf("the statement runs once the password is entered; last query %q", q)
	}
	if c.statusErr {
		t.Fatalf("status = %q", c.status)
	}
}

func TestConsoleFramesFollowFocus(t *testing.T) {
	c := newConsole(t, newFakeBackend(), "local")
	c.editor.SetValue("select * from users")
	runTab(t, c, c.Handle(keymap.RunStatement))
	lit := lipgloss.NewStyle().Foreground(theme.ColorFocus).Render("┌")
	plain := lipgloss.NewStyle().Foreground(theme.ColorBorder).Render("┌")
	frames := func() []string { // the editor's and the results' top-left corners
		var out []string
		for _, l := range strings.Split(c.Framed("tabs", true, false), "\n") {
			if strings.Contains(ansi.Strip(l), "┌") {
				out = append(out, l)
			}
		}
		return out
	}

	lines := strings.Split(ansi.Strip(c.Framed("tabs", true, false)), "\n")
	if len(lines) != 24+2 {
		t.Fatalf("%d lines, want 26", len(lines))
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w != 80+2 {
			t.Fatalf("line %q width %d, want 82", l, w)
		}
	}
	if f := frames(); len(f) != 2 || !strings.HasPrefix(f[0], lit) || !strings.HasPrefix(f[1], plain) ||
		!strings.Contains(ansi.Strip(f[1]), "Results · 2 rows") {
		t.Fatalf("editor focused, frames:\n%q", f)
	}
	if v := consoleView(c); !strings.Contains(v, "<ctrl+j> results") {
		t.Fatalf("editor hint:\n%s", v)
	}

	c.Handle(keymap.FocusResults)
	if f := frames(); !strings.HasPrefix(f[0], plain) || !strings.HasPrefix(f[1], lit) {
		t.Fatalf("results focused, frames:\n%q", f)
	}
	if v := c.View(true); strings.Contains(v, theme.Selected.Render("s")) || !strings.Contains(v, theme.SelectedDim.Render("s")) {
		t.Fatal("the editor cursor is underlined, not a block, while the results have focus")
	}
	if v := consoleView(c); !strings.Contains(v, "<ctrl+k> editor") {
		t.Fatalf("results hint:\n%s", v)
	}
}

const threeInserts = "insert into a values (1);\ninsert into b values (2);\ninsert into c values (3);"

func TestRunFromCursor(t *testing.T) {
	m, b := newTestModel(t)
	c := openLocalConsole(t, m)
	c.editor.SetValue(threeInserts)
	c.editor.SetCursor(1, 3)
	press(t, m, "space", "R")
	execs := b.conn.execList()
	want := []string{"insert into b values (2)", "insert into c values (3)"}
	if len(execs) < 2 || !slices.Equal(execs[len(execs)-2:], want) || slices.Contains(execs, "insert into a values (1)") {
		t.Fatalf("execs = %v", execs)
	}
}

func TestRunAllNamesTheFailedStatement(t *testing.T) {
	m, b := newTestModel(t)
	c := openLocalConsole(t, m)
	b.conn.setFail("insert into b values (2)", errors.New("boom"))
	c.editor.SetValue(threeInserts)
	press(t, m, "R")
	if !strings.Contains(c.status, "statement 2/3 failed") || !strings.Contains(c.status, "boom") {
		t.Fatalf("status = %q", c.status)
	}
	if got, want := c.editor.Offset(), strings.Index(threeInserts, "insert into b"); got != want {
		t.Fatalf("cursor at %d, want %d (the failed statement)", got, want)
	}
}

func TestConsoleHintsRunFromCursor(t *testing.T) {
	m, _ := newTestModel(t)
	c := openLocalConsole(t, m)
	c.SetSize(200, 20)
	if !strings.Contains(c.statusLine(), "run from here") {
		t.Fatalf("hints: %s", c.statusLine())
	}
}
