package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

func newTestModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	b := newFakeBackend()
	m := New(b, config.Settings{PageSize: 2, MaxColumnWidth: 40})
	m.whichKeyDelay = 0
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
	press(t, m, "l", "j", "l", "j", "l")
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
	press(t, m, "j", "l")
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
	press(t, m, "ctrl+h", "l")
	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d, want 1", len(m.tabs))
	}
}

func TestStructureViaLeader(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l", "j", "l", "j") // cursor on users
	press(t, m, "space", "s")
	s := screen(m)
	if !strings.Contains(s, "users:struct") || !strings.Contains(s, "[Columns]") {
		t.Fatalf("structure tab:\n%s", s)
	}
	press(t, m, "space", "s") // from the structure tab: same table, no new tab
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
	if !strings.Contains(screen(m), "Open structure") {
		t.Fatalf("which-key not shown:\n%s", screen(m))
	}
	press(t, m, "esc")
	if strings.Contains(screen(m), "Open structure") {
		t.Fatal("esc cancels the pending sequence")
	}
}

func TestHelpOverlay(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "?")
	if s := screen(m); !strings.Contains(s, "Help") || !strings.Contains(s, "Expand / open") {
		t.Fatalf("help:\n%s", s)
	}
	press(t, m, "esc")
	if strings.Contains(screen(m), "Expand / open") {
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
	if !strings.Contains(screen(m), ":█") {
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
	press(t, m, "ctrl+h", "j", "l") // open view "active"
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
	press(t, m, "space", "b")
	if !strings.Contains(screen(m), "Connections") {
		t.Fatal("the tree stays visible while there are no tabs")
	}
	openUsers(t, m)
	press(t, m, "space", "b")
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
