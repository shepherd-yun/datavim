package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/rdb"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

func TestConsoleScriptShowsEveryStatement(t *testing.T) {
	b := newFakeBackend()
	b.conn.setFail("select bad", errors.New("syntax error near bad"))
	c := newConsole(t, b, "local")
	c.editor.SetValue("select 1;\nupdate t set a = 1 where id = 1;\nselect bad;\nselect 2")
	runTab(t, c, c.Handle(keymap.RunAll))
	if len(c.stmts) != 3 || c.tabs.cur != 3 {
		t.Fatalf("tabs %v, on %d: the failed statement is shown", c.tabs.labels, c.tabs.cur)
	}
	if v := consoleView(c); !strings.Contains(v, "[3 SELECT ✗]") || !strings.Contains(v, "syntax error near bad") {
		t.Fatalf("view:\n%s", v)
	}
	c.Handle(keymap.FocusResults)
	c.Handle(keymap.PrevResult)
	c.Handle(keymap.PrevResult)
	if v := consoleView(c); !strings.Contains(v, "[1 SELECT]") || !strings.Contains(v, "user1") {
		t.Fatalf("the first statement's rows:\n%s", v)
	}
	c.Handle(keymap.ResultView)
	if v := consoleView(c); !strings.Contains(v, "✓ statement 1/3 · 2 rows") {
		t.Fatalf("its message:\n%s", v)
	}
	c.Handle(keymap.ResultView) // [ and ] cycle back to the rows
	if !c.dataShown() {
		t.Fatal("back to the rows")
	}
}

func TestConsoleSeveralWritesShowSummary(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.editor.SetValue("update t set a = 1 where id = 1; update t set a = 2 where id = 2")
	runTab(t, c, c.Handle(keymap.RunAll))
	if c.tabs.cur != 0 || !strings.Contains(consoleView(c), "[Summary]") {
		t.Fatalf("on %d:\n%s", c.tabs.cur, consoleView(c))
	}
	c.Handle(keymap.FocusResults)
	c.Handle(keymap.NextResult)
	c.Handle(keymap.ResultView)
	if c.dataShown() || !strings.Contains(c.status, "no rows") {
		t.Fatalf("a write has only its message: %q", c.status)
	}
}

func TestConsoleLoadsMoreAtTheLastRow(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	runSQL(t, c, "select * from users") // 3 rows, max_rows 2
	c.Handle(keymap.FocusResults)
	if cmd := c.Handle(keymap.Down); cmd == nil {
		t.Fatal("moving onto the last row loads more")
	} else {
		runTab(t, c, cmd)
	}
	if n := c.grid.RowCount(); n != 3 {
		t.Fatalf("rows = %d", n)
	}
	if cmd := c.Handle(keymap.Down); cmd != nil {
		t.Fatal("all rows are loaded")
	}
}

func TestConsoleStagedChangesKeepTheirTab(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	stageName(t, c)
	c.Handle(keymap.NextResult)
	if c.tabs.cur != 1 || !strings.Contains(c.status, "change") {
		t.Fatalf("on %d, status %q", c.tabs.cur, c.status)
	}
	c.Handle(keymap.ResultView)
	if !c.dataShown() {
		t.Fatal("the staged rows stay on screen")
	}
}

func TestRedisConsoleShowsEveryCommand(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	runR(t, m, c, "SET a 1\nINCR n\nGET a", keymap.RunAll)
	if len(c.cmds) != 3 || c.tabs.cur != 3 {
		t.Fatalf("tabs %v, on %d", c.tabs.labels, c.tabs.cur)
	}
	c.Handle(keymap.FocusResults)
	c.Handle(keymap.PrevResult)
	if v := ansi.Strip(c.View(true)); !strings.Contains(v, "[2 INCR]") || !strings.Contains(v, "(integer) 1") {
		t.Fatalf("view:\n%s", v)
	}
	c.Handle(keymap.ResultView)
	if v := ansi.Strip(c.View(true)); !strings.Contains(v, "✓ command 2/3") || !strings.Contains(v, "INCR n") {
		t.Fatalf("message:\n%s", v)
	}
	c.Handle(keymap.NextResult)
	c.Handle(keymap.NextResult) // wraps to Summary
	if c.resultsTitle() != "Summary" {
		t.Fatalf("title %q", c.resultsTitle())
	}
}

func TestHelpInResultsListsItsOwnKeys(t *testing.T) {
	m, _ := newTestModel(t)
	openLocalConsole(t, m)
	press(t, m, "ctrl+j", "?")
	h, ok := m.overlay.(*overlay.Help)
	if !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	v := ansi.Strip(h.View(120, 200))
	for _, want := range []string{"Load more rows", "Next result tab", "Go to tab 1–9"} {
		if !strings.Contains(v, want) {
			t.Errorf("help lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "Export as CSV") || strings.Contains(v, "Go to tab 2") {
		t.Fatalf("help lists the global keys of other areas:\n%s", v)
	}
}

func TestRedisConsoleReadsAndCopiesFastJSON(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	clip := captureClipboard(t)
	c := openRConsole(t, m, 0)
	c.editor.SetValue("INCR n")
	drainAll(t, m, c.Handle(keymap.RunStatement)) // opens the session
	raw := `[{"@type":"a.Dict","dictCode":28L,"label":"成功"}]`
	b.lastSession().setAnswer(func(args []string) (rdb.Reply, bool) {
		return rdb.Reply{Kind: rdb.KindString, Str: raw}, args[0] == "GET"
	})
	runR(t, m, c, "GET d", keymap.RunStatement)
	if v := strings.Join(c.text, "\n"); !strings.Contains(v, `    "dictCode": 28,`) {
		t.Fatalf("indented:\n%s", v)
	}
	press(t, m, "ctrl+j", "y", "y")
	if *clip != raw {
		t.Fatalf("clipboard = %q, want the value as read", *clip)
	}
	press(t, m, "]", "y", "y") // the message
	if !strings.Contains(*clip, "✓ command 1/1") || !strings.HasSuffix(*clip, "GET d") {
		t.Fatalf("clipboard = %q", *clip)
	}
}
