package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

func newTestModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	b := newFakeBackend()
	m := New(b, newFakeStore(), config.Settings{PageSize: 2, MaxColumnWidth: 40, MaxRows: 10})
	m.whichKeyDelay = 0
	m.timings = timings{}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m, b
}

// drain runs cmd and feeds every resulting message back into m.
// It reports whether a tea.QuitMsg was produced.
func drain(t *testing.T, m *Model, cmd tea.Cmd) (quit bool) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 100 {
			t.Fatal("drain: too many steps")
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
		case tea.QuitMsg:
			quit = true
		default:
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
	return quit
}

func press(t *testing.T, m *Model, keys ...string) (quit bool) {
	t.Helper()
	for _, k := range keys {
		_, cmd := m.Update(uitest.Key(k))
		quit = drain(t, m, cmd) || quit
	}
	return quit
}

func screen(m *Model) string { return ansi.Strip(m.render()) }

// openUsers expands local -> app and opens the users table.
func openUsers(t *testing.T, m *Model) {
	t.Helper()
	press(t, m, "l", "j", "l", "j", "enter")
}

func TestStartsWithConnections(t *testing.T) {
	m, _ := newTestModel(t)
	s := screen(m)
	for _, want := range []string{"Connections", "local", "prod", "[PROD]", "NORMAL", "? help"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen missing %q:\n%s", want, s)
		}
	}
}

func TestBrowseToTableData(t *testing.T) {
	m, b := newTestModel(t)
	press(t, m, "l")
	if !strings.Contains(screen(m), "app") {
		t.Fatalf("databases not loaded:\n%s", screen(m))
	}
	press(t, m, "j", "l")
	if s := screen(m); !strings.Contains(s, "users") || !strings.Contains(s, "active") {
		t.Fatalf("tables not loaded:\n%s", s)
	}
	press(t, m, "j", "enter")
	s := screen(m)
	if !strings.Contains(s, "[1]users") || !strings.Contains(s, "user1") {
		t.Fatalf("data tab not shown:\n%s", s)
	}
	if m.focus != focusTabs {
		t.Error("opening a table focuses the tab area")
	}
	if !strings.Contains(b.conn.lastQuery(), "LIMIT 3 OFFSET 0") {
		t.Errorf("query = %q", b.conn.lastQuery())
	}
	if !strings.Contains(s, "local/app") {
		t.Errorf("status bar shows the location:\n%s", s)
	}
}

func TestReopeningATableReusesItsTab(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, "ctrl+h", "enter")
	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d, want 1", len(m.tabs))
	}
}

func TestColumnsPopupFromTreeOpensDataAtColumn(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l", "j", "l", "j") // cursor on users
	press(t, m, "space", "s")
	if _, ok := m.overlay.(*overlay.Columns); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	if s := screen(m); !strings.Contains(s, "PK") || !strings.Contains(s, "2 columns") {
		t.Fatalf("columns popup:\n%s", s)
	}
	press(t, m, "n", "a", "m", "e", "enter")
	tab, ok := m.activeTab().(*dataTab)
	if !ok || tab.ref.Name != "users" {
		t.Fatalf("active tab = %#v", m.activeTab())
	}
	if c, ok := tab.grid.Column(); !ok || c.Name != "name" {
		t.Fatalf("cursor on %q", c.Name)
	}
}

func TestColumnsPopupMovesTheDataTabCursor(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	tab := m.activeTab().(*dataTab)
	press(t, m, "l") // cursor on name
	press(t, m, "space", "s", "up", "enter")
	if c, _ := tab.grid.Column(); c.Name != "id" {
		t.Fatalf("cursor on %q", c.Name)
	}
	if row, _ := tab.grid.Position(); row != 0 || len(m.tabs) != 1 {
		t.Fatalf("row = %d, tabs = %d", row, len(m.tabs))
	}
}

func TestColumnsPopupOpensTheStructure(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l", "j", "l", "j") // cursor on users
	press(t, m, "space", "s", "ctrl+o")
	s := screen(m)
	if !strings.Contains(s, "users:struct") || !strings.Contains(s, "[Columns]") {
		t.Fatalf("structure tab:\n%s", s)
	}
	press(t, m, "space", "s", "ctrl+o") // from the structure tab: same table, no new tab
	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d", len(m.tabs))
	}
}

func TestStructureNeedsATable(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "space", "s")
	if !strings.Contains(screen(m), "select a table first") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestWhichKey(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "space")
	if !strings.Contains(screen(m), "Columns / structure") {
		t.Fatalf("which-key not shown:\n%s", screen(m))
	}
	press(t, m, "esc")
	if strings.Contains(screen(m), "Columns / structure") {
		t.Fatal("esc cancels the pending sequence")
	}
}

func TestHelpOverlay(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "?")
	if s := screen(m); !strings.Contains(s, "Help") || !strings.Contains(s, "Expand") {
		t.Fatalf("help:\n%s", s)
	}
	press(t, m, "esc")
	if strings.Contains(screen(m), "Expand") {
		t.Fatal("esc closes help")
	}
}

func TestPasswordFlow(t *testing.T) {
	m, b := newTestModel(t)
	b.needPassword = true
	press(t, m, "l")
	if !strings.Contains(screen(m), "Password for") {
		t.Fatalf("prompt not shown:\n%s", screen(m))
	}
	press(t, m, "s", "e", "c", "enter")
	if !slices.Equal(b.passwords, []string{"local:sec:true"}) {
		t.Fatalf("passwords = %v", b.passwords)
	}
	if !strings.Contains(screen(m), "app") {
		t.Fatalf("load must resume after the password:\n%s", screen(m))
	}
}

func TestPasswordKeyResetsConnection(t *testing.T) {
	m, b := newTestModel(t)
	press(t, m, "l", "p", "x", "enter")
	if !slices.Equal(b.passwords, []string{"local:x:true"}) {
		t.Fatalf("passwords = %v", b.passwords)
	}
	if strings.Contains(screen(m), "app") {
		t.Fatal("the connection subtree is reset after changing its password")
	}
}

func TestConnectionErrorIsShownAndRetried(t *testing.T) {
	m, b := newTestModel(t)
	b.setConnErr(errors.New("dial tcp: connection refused"))
	press(t, m, "l")
	if !strings.Contains(screen(m), "connection refused") {
		t.Fatalf("error not shown:\n%s", screen(m))
	}
	b.setConnErr(nil)
	press(t, m, "l")
	if !strings.Contains(screen(m), "app") {
		t.Fatalf("retry failed:\n%s", screen(m))
	}
}

func TestCommandLine(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, ":")
	if lines := strings.Split(screen(m), "\n"); m.cmdline == nil || !strings.HasPrefix(lines[len(lines)-1], ":") {
		t.Fatal("command line shown in the status bar")
	}
	press(t, m, "q", "enter")
	if len(m.tabs) != 0 || m.focus != focusTree {
		t.Fatalf(":q closes the tab; tabs=%d focus=%v", len(m.tabs), m.focus)
	}
	press(t, m, ":", "n", "o", "p", "e", "enter")
	if !strings.Contains(screen(m), "unknown command: nope") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	if !press(t, m, ":", "q", "a", "enter") {
		t.Fatal(":qa quits")
	}
}

func TestFocusAndTabs(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, "ctrl+h")
	if m.focus != focusTree {
		t.Fatal("ctrl+h focuses the tree")
	}
	press(t, m, "tab")
	if m.focus != focusTabs {
		t.Fatal("tab toggles focus")
	}
	press(t, m, "ctrl+h", "ctrl+h")
	if m.focus != focusTabs {
		t.Fatal("ctrl+h on the tree wraps to the tabs")
	}
	press(t, m, "ctrl+l")
	if m.focus != focusTree {
		t.Fatal("ctrl+l on the tabs wraps to the tree")
	}
	press(t, m, "ctrl+l")
	if m.focus != focusTabs {
		t.Fatal("ctrl+l focuses the tabs")
	}
	press(t, m, "ctrl+h", "j", "enter") // open view "active"
	if len(m.tabs) != 2 || m.active != 1 {
		t.Fatalf("tabs=%d active=%d", len(m.tabs), m.active)
	}
	press(t, m, "1")
	if m.active != 0 || m.focus != focusTabs {
		t.Fatalf("1 jumps to the first tab; active=%d", m.active)
	}
	press(t, m, "9")
	if m.active != 0 {
		t.Fatal("jumping to a missing tab is a no-op")
	}
}

func TestToggleTree(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "space", "e")
	if !strings.Contains(screen(m), "Connections") {
		t.Fatal("the tree stays visible while there are no tabs")
	}
	openUsers(t, m)
	press(t, m, "space", "e")
	if strings.Contains(screen(m), "Connections") {
		t.Fatal("tree hidden")
	}
	press(t, m, "ctrl+h")
	if !strings.Contains(screen(m), "Connections") || m.focus != focusTree {
		t.Fatal("focusing the hidden tree shows it")
	}
}

func TestTinySizesDoNotPanic(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, "?")
	for _, size := range [][2]int{{0, 0}, {1, 1}, {10, 3}, {20, 5}, {200, 60}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		_ = m.render()
	}
}

func typeKeys(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		press(t, m, string(r))
	}
}

func TestConsoleFocusWraps(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l", "j", "space", "c")
	c := m.activeTab().(*consoleTab)
	press(t, m, "ctrl+k")
	if !c.onResults {
		t.Fatal("ctrl+k on the editor wraps to the results")
	}
	press(t, m, "ctrl+j")
	if c.onResults {
		t.Fatal("ctrl+j on the results wraps to the editor")
	}
	press(t, m, "ctrl+j")
	if !c.onResults {
		t.Fatal("ctrl+j focuses the results")
	}
	press(t, m, "ctrl+k")
	if c.onResults {
		t.Fatal("ctrl+k focuses the editor")
	}
}

func TestOpenConsoleTypeAndRun(t *testing.T) {
	m, b := newTestModel(t)
	press(t, m, "l", "j") // cursor on database "app"
	press(t, m, "space", "c")
	if s := screen(m); !strings.Contains(s, "[1]console:app") {
		t.Fatalf("console tab:\n%s", s)
	}
	press(t, m, "i")
	if !strings.Contains(screen(m), " INSERT ") {
		t.Fatalf("status shows INSERT:\n%s", screen(m))
	}
	typeKeys(t, m, "select 1")
	press(t, m, "1") // digits are text in INSERT, not "go to tab"
	press(t, m, "esc", "enter")
	if got := b.conn.lastQuery(); got != "select 11" {
		t.Fatalf("ran %q", got)
	}
	if s := screen(m); !strings.Contains(s, "user1") || !strings.Contains(s, " NORMAL ") {
		t.Fatalf("result:\n%s", s)
	}
	press(t, m, "ctrl+h", "space", "c")
	if len(m.tabs) != 1 {
		t.Fatal("<space>c on the same database reuses the console")
	}
}

func TestPasteGoesToTheEditorInInsertMode(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l", "j", "space", "c", "i")
	m.Update(tea.PasteMsg{Content: "select 42"})
	c := m.activeTab().(*consoleTab)
	if c.editor.Value() != "select 42" {
		t.Fatalf("editor = %q", c.editor.Value())
	}
}

func TestPasteGoesToTheEditorInNormalMode(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l", "j", "space", "c")
	m.Update(tea.PasteMsg{Content: "select 42"})
	c := m.activeTab().(*consoleTab)
	if c.editor.Value() != "select 42" || c.editor.Insert() {
		t.Fatalf("editor = %q, mode %v", c.editor.Value(), c.editor.Mode())
	}
}

func TestCtrlCCancelsButNeverQuits(t *testing.T) {
	m, _ := newTestModel(t)
	if press(t, m, "ctrl+c") {
		t.Fatal("ctrl+c must not quit")
	}
	if !strings.Contains(screen(m), ":qa quits") {
		t.Fatalf("hint:\n%s", screen(m))
	}
}

func TestConsoleNeedsAConnection(t *testing.T) {
	b := newFakeBackend()
	b.conns = nil
	m := New(b, newFakeStore(), config.Settings{PageSize: 2, MaxColumnWidth: 40, MaxRows: 10})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.whichKeyDelay = 0
	press(t, m, "space", "c")
	if !strings.Contains(screen(m), "select a connection first") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestSearchTreeAndGrid(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "/")
	if lines := strings.Split(screen(m), "\n"); m.cmdline == nil || !strings.HasPrefix(lines[len(lines)-1], "/") {
		t.Fatal("search line in the status bar")
	}
	press(t, m, "p", "r", "enter")
	if m.tree.Selected().Name != "prod" {
		t.Fatalf("selected %q", m.tree.Selected().Name)
	}
	press(t, m, "n")
	if m.tree.Selected().Name != "prod" {
		t.Fatal("n repeats (only one match)")
	}
	press(t, m, "/", "z", "z", "enter")
	if !strings.Contains(screen(m), "pattern not found: zz") {
		t.Fatalf("screen:\n%s", screen(m))
	}

	press(t, m, "g", "g") // back to the first connection
	openUsers(t, m)
	press(t, m, "/", "u", "s", "e", "r", "2", "enter")
	tab := m.activeTab().(*dataTab)
	if _, v, _ := tab.grid.Cell(); v.Text != "user2" {
		t.Fatalf("grid search at %q", v.Text)
	}
	press(t, m, "N")
	if _, v, _ := tab.grid.Cell(); v.Text != "user2" {
		t.Fatal("N wraps back to the only match")
	}
}

func TestQuitClosesConsoleSessions(t *testing.T) {
	m, b := newTestModel(t)
	press(t, m, "l", "j", "space", "c", "enter") // empty editor: nothing runs
	c := m.activeTab().(*consoleTab)
	c.editor.SetValue("select 1")
	press(t, m, "enter")
	if b.conn.sessionCount() != 1 {
		t.Fatal("a session was opened")
	}
	if !press(t, m, ":", "q", "a", "enter") {
		t.Fatal(":qa quits")
	}
	if !b.conn.sessions[0].closed {
		t.Fatal("sessions are closed on quit")
	}
}

func TestCloseTabClosesItsSession(t *testing.T) {
	m, b := newTestModel(t)
	press(t, m, "l", "j", "space", "c")
	m.activeTab().(*consoleTab).editor.SetValue("select 1")
	press(t, m, "enter", ":", "q", "enter")
	if len(m.tabs) != 0 || !b.conn.sessions[0].closed {
		t.Fatal(":q closes the console and its session")
	}
}

func TestLateRunResultForClosedTabClosesSession(t *testing.T) {
	m, _ := newTestModel(t)
	s := &fakeSession{conn: &fakeConn{}}
	_, cmd := m.Update(runDoneMsg{tabID: 99, session: s})
	drain(t, m, cmd)
	if !s.closed {
		t.Fatal("orphaned sessions are closed")
	}
}

func TestStructureFromConsoleNeedsATable(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l", "j", "space", "c", "space", "s")
	if !strings.Contains(screen(m), "select a table first") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestHistoryPicker(t *testing.T) {
	m, _ := newTestModel(t)
	st := m.store.(*fakeStore)
	st.entries = []history.Entry{{Time: time.Now(), Conn: "local", Database: "app", SQL: "select 7"}}
	press(t, m, "space", "h")
	if s := screen(m); !strings.Contains(s, "SQL history") || !strings.Contains(s, "select 7") {
		t.Fatalf("picker:\n%s", s)
	}
	press(t, m, "enter")
	c, ok := m.activeTab().(*consoleTab)
	if !ok || c.editor.Value() != "select 7" || c.ns.Database != "app" {
		t.Fatalf("console = %#v", m.activeTab())
	}
	press(t, m, "space", "h", "enter")
	if c.editor.Value() != "select 7\nselect 7" {
		t.Fatalf("appended to the open console: %q", c.editor.Value())
	}
}

func TestHistoryEmptyAndMissingConnection(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "space", "h")
	if !strings.Contains(screen(m), "no SQL history yet") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	m.store.(*fakeStore).entries = []history.Entry{{Conn: "gone", SQL: "select 1"}}
	press(t, m, "space", "h", "enter")
	if !strings.Contains(screen(m), "connection gone no longer exists") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestNewConnectionForm(t *testing.T) {
	m, b := newTestModel(t)
	press(t, m, "space", "n")
	if !strings.Contains(screen(m), "New connection") {
		t.Fatalf("form:\n%s", screen(m))
	}
	press(t, m, "n", "e", "w", "ctrl+s")
	if len(b.added) != 1 || b.added[0].Name != "new" {
		t.Fatalf("added = %+v", b.added)
	}
	if s := screen(m); !strings.Contains(s, "added connection new") {
		t.Fatalf("screen:\n%s", s)
	}
	if n := m.tree.Selected(); n == nil || n.Name != "new" || m.focus != focusTree {
		t.Fatal("the new connection is selected in the tree")
	}
}

func TestNewConnectionError(t *testing.T) {
	m, b := newTestModel(t)
	b.addErr = errors.New("connection \"local\" already exists")
	press(t, m, "space", "n", "l", "o", "c", "a", "l", "ctrl+s")
	if s := screen(m); !strings.Contains(s, "already exists") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestEmptyConfigShowsHowToAddAConnection(t *testing.T) {
	b := newFakeBackend()
	b.conns = nil
	m := New(b, newFakeStore(), config.Settings{PageSize: 2, MaxColumnWidth: 40, MaxRows: 10})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if s := screen(m); !strings.Contains(s, "No connections") || !strings.Contains(s, "<space>n") {
		t.Fatalf("screen:\n%s", s)
	}
}

func statusLine(m *Model) string {
	lines := strings.Split(screen(m), "\n")
	return lines[len(lines)-1]
}

// openProdUsers expands prod -> app and opens the users table.
func openProdUsers(t *testing.T, m *Model) {
	t.Helper()
	press(t, m, "j", "l", "j", "l", "j", "enter")
}

func TestProdConnectionIsRed(t *testing.T) {
	redCorner := lipgloss.NewStyle().Foreground(theme.ColorError).Render("┌")

	dev, _ := newTestModel(t)
	openUsers(t, dev)
	if s := statusLine(dev); strings.Contains(s, "[PROD]") || strings.Contains(s, " RO ") {
		t.Fatalf("dev status: %s", s)
	}
	if strings.Contains(dev.render(), redCorner) {
		t.Fatal("dev tabs are not framed in red")
	}

	prod, _ := newTestModel(t)
	openProdUsers(t, prod)
	if _, ok := prod.activeTab().(*dataTab); !ok {
		t.Fatalf("prod users not opened:\n%s", screen(prod))
	}
	if s := statusLine(prod); !strings.Contains(s, "[PROD]") || !strings.Contains(s, " RO ") {
		t.Fatalf("prod status: %s", s)
	}
	out := prod.render()
	if !strings.Contains(out, redCorner) || !strings.Contains(out, theme.ProdMode.Render(" NORMAL ")) {
		t.Fatal("prod tab frame and mode block are red")
	}
	press(t, prod, "ctrl+h", "g", "g") // tree focus on local: the status bar follows the tree selection
	if s := statusLine(prod); strings.Contains(s, "[PROD]") {
		t.Fatalf("status for local: %s", s)
	}
	faintRedCorner := lipgloss.NewStyle().Foreground(theme.ColorError).Faint(true).Render("┌")
	if !strings.Contains(prod.render(), faintRedCorner) {
		t.Fatal("the tab area stays (faint) red while it shows a prod tab")
	}
}

// command types :text and enter.
func command(t *testing.T, m *Model, text string) (quit bool) {
	t.Helper()
	keys := []string{":"}
	for _, r := range text {
		if r == ' ' {
			keys = append(keys, "space")
		} else {
			keys = append(keys, string(r))
		}
	}
	return press(t, m, append(keys, "enter")...)
}

// openLocalConsole opens a console on the local connection from the tree.
func openLocalConsole(t *testing.T, m *Model) *consoleTab {
	t.Helper()
	press(t, m, "space", "c")
	c, ok := m.activeTab().(*consoleTab)
	if !ok {
		t.Fatalf("console not opened:\n%s", screen(m))
	}
	return c
}

func runInConsole(t *testing.T, m *Model, c *consoleTab, sql string) {
	t.Helper()
	c.editor.SetValue(sql)
	drain(t, m, c.Handle(keymap.RunStatement))
}

// openTxConsole opens a local console in manual commit with one uncommitted insert.
func openTxConsole(t *testing.T, m *Model) *consoleTab {
	t.Helper()
	c := openLocalConsole(t, m)
	press(t, m, "space", "t")
	runInConsole(t, m, c, "insert into t values (1)")
	if !c.openTx() {
		t.Fatal("transaction not open")
	}
	return c
}

func TestToggleTxNeedsAConsole(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "space", "t")
	if s := statusLine(m); !strings.Contains(s, "console") {
		t.Fatalf("status: %s", s)
	}
}

func TestManualCommitFromTheUI(t *testing.T) {
	m, b := newTestModel(t)
	c := openLocalConsole(t, m)
	press(t, m, "space", "t")
	if !c.manualTx || !strings.Contains(statusLine(m), "TX") {
		t.Fatalf("manual=%v status: %s", c.manualTx, statusLine(m))
	}
	runInConsole(t, m, c, "insert into t values (1)")
	if s := statusLine(m); !strings.Contains(s, "TX 1") {
		t.Fatalf("status: %s", s)
	}
	command(t, m, "commit")
	if got := b.conn.execList(); got[len(got)-1] != "COMMIT" {
		t.Fatalf("execs = %v", got)
	}
	if s := statusLine(m); strings.Contains(s, "TX 1") {
		t.Fatalf("status after commit: %s", s)
	}
	command(t, m, "rollback")
	if !strings.Contains(screen(m), "no open transaction") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestCommitOutsideAConsole(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	command(t, m, "commit")
	if s := statusLine(m); !strings.Contains(s, "not a console") {
		t.Fatalf("status: %s", s)
	}
}

func TestQuitAsksWithOpenTransaction(t *testing.T) {
	m, _ := newTestModel(t)
	openTxConsole(t, m)
	if command(t, m, "qa") {
		t.Fatal(":qa asks first")
	}
	if !strings.Contains(screen(m), "Open transactions") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	if press(t, m, "n") {
		t.Fatal("n keeps sqlvim running")
	}
	command(t, m, "qa")
	if !press(t, m, "y") {
		t.Fatal("y quits")
	}

	forced, _ := newTestModel(t)
	openTxConsole(t, forced)
	if !command(t, forced, "qa!") {
		t.Fatal(":qa! quits without asking")
	}
}

func TestCloseTabAsksWithOpenTransaction(t *testing.T) {
	m, _ := newTestModel(t)
	openTxConsole(t, m)
	command(t, m, "q")
	if len(m.tabs) != 1 || !strings.Contains(screen(m), "Open transaction") {
		t.Fatalf("tabs=%d screen:\n%s", len(m.tabs), screen(m))
	}
	press(t, m, "y")
	if len(m.tabs) != 0 {
		t.Fatal("y closes the console")
	}

	forced, _ := newTestModel(t)
	openTxConsole(t, forced)
	command(t, forced, "q!")
	if len(forced.tabs) != 0 {
		t.Fatal(":q! closes without asking")
	}
}

func TestSetReadOnlyCommands(t *testing.T) {
	m, b := newTestModel(t)
	press(t, m, "j") // prod: read-only by default
	command(t, m, "set noreadonly")
	if cfg, _ := b.Connection("prod"); cfg.IsReadOnly() {
		t.Fatal(":set noreadonly unlocks the selected connection")
	}
	if s := statusLine(m); strings.Contains(s, " RO ") || !strings.Contains(s, "read-write") {
		t.Fatalf("status: %s", s)
	}
	command(t, m, "set ro")
	if cfg, _ := b.Connection("prod"); !cfg.IsReadOnly() {
		t.Fatal(":set ro locks it again")
	}
	command(t, m, "set ro")
	if s := statusLine(m); !strings.Contains(s, "already") {
		t.Fatalf("status: %s", s)
	}
	command(t, m, "set bogus")
	if s := statusLine(m); !strings.Contains(s, "unknown option") {
		t.Fatalf("status: %s", s)
	}
}

func TestSetReadOnlyBlockedByOpenTransaction(t *testing.T) {
	m, b := newTestModel(t)
	openTxConsole(t, m)
	command(t, m, "set readonly")
	if cfg, _ := b.Connection("local"); cfg.IsReadOnly() {
		t.Fatal("not switched while a transaction is open")
	}
	if s := statusLine(m); !strings.Contains(s, "open transaction") {
		t.Fatalf("status: %s", s)
	}
}

func TestPasteGoesToTheOpenPopup(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "space", "n")
	m.Update(tea.PasteMsg{Content: "pasted-name"})
	if !strings.Contains(screen(m), "pasted-name") {
		t.Fatalf("the new connection form takes pasted text:\n%s", screen(m))
	}
	press(t, m, "esc", ":")
	m.Update(tea.PasteMsg{Content: "qa"})
	if !press(t, m, "enter") {
		t.Fatal("the command line takes pasted text")
	}
}

func TestEditConnection(t *testing.T) {
	m, b := newTestModel(t)
	openUsers(t, m) // a tab on local
	press(t, m, "ctrl+h", "g", "g", "e")
	if !strings.Contains(screen(m), "Edit connection") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	press(t, m, "2", "ctrl+s") // rename local -> local2
	if _, ok := b.Connection("local2"); !ok {
		t.Fatalf("connections = %+v", b.Connections())
	}
	if len(m.tabs) != 0 {
		t.Fatal("tabs on the edited connection are closed")
	}
	if n := m.tree.Selected(); n == nil || n.Name != "local2" {
		t.Fatalf("selected = %+v", n)
	}
	if s := statusLine(m); !strings.Contains(s, "local2") {
		t.Fatalf("status: %s", s)
	}
	press(t, m, "l") // the renamed connection loads
	if !strings.Contains(screen(m), "app") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestDeleteConnectionNeedsItsName(t *testing.T) {
	m, b := newTestModel(t)
	press(t, m, "d", "d")
	if !strings.Contains(screen(m), "Delete connection") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	press(t, m, "y", "enter")
	if len(b.Connections()) != 2 {
		t.Fatal("y alone does not delete")
	}
	press(t, m, "ctrl+u", "l", "o", "c", "a", "l", "enter")
	if len(b.Connections()) != 1 {
		t.Fatalf("connections = %+v", b.Connections())
	}
	if n := m.tree.Selected(); n == nil || n.Name != "prod" {
		t.Fatalf("selected = %+v", n)
	}
	if strings.Contains(screen(m), "local") && !strings.Contains(statusLine(m), "deleted") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestEditAndDeleteBlockedByOpenTransaction(t *testing.T) {
	m, b := newTestModel(t)
	openTxConsole(t, m)
	press(t, m, "ctrl+h", "g", "g", "e")
	if strings.Contains(screen(m), "Edit connection") || !strings.Contains(statusLine(m), "open transaction") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	press(t, m, "d", "d")
	if strings.Contains(screen(m), "Delete connection") || len(b.Connections()) != 2 {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestEditConnectionSaveFailureKeepsEverything(t *testing.T) {
	m, b := newTestModel(t)
	openUsers(t, m)
	b.addErr = errors.New("connections.toml: permission denied")
	press(t, m, "ctrl+h", "g", "g", "e", "ctrl+s") // same name, nothing renamed
	if s := statusLine(m); !strings.Contains(s, "cannot save connection") {
		t.Fatalf("status: %s", s)
	}
	if len(m.tabs) != 1 {
		t.Fatal("tabs stay open when nothing was saved")
	}
}

func TestLocateRevealsTheActiveTable(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, "ctrl+h", "g", "g", "ctrl+l") // move the tree away, back to the tab
	press(t, m, "space", "l")
	if n := m.tree.Selected(); m.focus != focusTree || n == nil || n.Name != "users" || n.Conn != "local" {
		t.Fatalf("focus %v, selected %+v", m.focus, n)
	}
}

func TestLocateLoadsTheTreeOnTheWay(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	m.tree.ResetConn("local")
	m.setFocus(focusTabs)
	press(t, m, "space", "l")
	if n := m.tree.Selected(); n == nil || n.Name != "users" {
		t.Fatalf("selected %+v", n)
	}
}

func TestLocateShowsAHiddenTree(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, "space", "e")
	if m.showTree {
		t.Fatal("tree still shown")
	}
	press(t, m, "space", "l")
	if n := m.tree.Selected(); !m.showTree || n == nil || n.Name != "users" {
		t.Fatalf("shown %v, selected %+v", m.showTree, n)
	}
}

func TestLocateRevealsTheConsoleNamespace(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, "ctrl+h", "k") // tree on app
	c := openLocalConsole(t, m)
	if c.ns.Database != "app" {
		t.Fatalf("console ns %+v", c.ns)
	}
	press(t, m, "ctrl+h", "g", "g", "ctrl+l")
	command(t, m, "locate")
	if n := m.tree.Selected(); m.focus != focusTree || n == nil || n.Name != "app" {
		t.Fatalf("focus %v, selected %+v", m.focus, n)
	}
}

func TestLocateWithoutTabOrMissingNode(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "space", "l")
	if s := statusLine(m); !strings.Contains(s, "no tab") {
		t.Fatalf("status: %s", s)
	}
	openUsers(t, m)
	m.tabs[m.active].(*dataTab).ref.Name = "gone"
	press(t, m, "space", "l")
	if s := statusLine(m); !strings.Contains(s, "not in the tree") {
		t.Fatalf("status: %s", s)
	}
}
