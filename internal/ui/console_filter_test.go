package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
)

// filterFirstID runs sql and filters its result by the first row's id.
func filterFirstID(t *testing.T, c *consoleTab, sql string) {
	t.Helper()
	c.onResults = false
	runSQL(t, c, sql)
	c.onResults = true
	c.grid.SetCursor(0, 0)
	runTab(t, c, c.Handle(keymap.FilterEqual))
}

func TestConsoleFilterMergesIntoWhere(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	filterFirstID(t, c, "select * from users")
	want := "select * from users\nWHERE (`id` = '1'\n)"
	if got := b.conn.lastQuery(); got != want {
		t.Fatalf("ran %q, want %q", got, want)
	}
	if c.shown.where != "`id` = '1'" || !strings.Contains(consoleView(c), "where `id` = '1'") {
		t.Fatalf("where %q:\n%s", c.shown.where, consoleView(c))
	}
	runTab(t, c, c.Handle(keymap.FilterClear))
	if got := b.conn.lastQuery(); got != "select * from users" || c.shown.where != "" {
		t.Fatalf("fx: ran %q, where %q", got, c.shown.where)
	}
}

func TestConsoleFilterWrapsOtherQueries(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	filterFirstID(t, c, "select id, name from users u join t on t.id = u.id order by name")
	want := "SELECT * FROM (\nselect id, name from users u join t on t.id = u.id\n) q WHERE (`id` = '1'\n)\norder by name"
	if got := b.conn.lastQuery(); got != want {
		t.Fatalf("ran %q", got)
	}

	c = newConsole(t, b, "local")
	filterFirstID(t, c, "select * from a join b order by a.x")
	if !strings.Contains(c.status, "order may not be kept") {
		t.Fatalf("status = %q", c.status)
	}
}

func TestConsoleFilterKeptByLoadMoreDroppedByNewRun(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	filterFirstID(t, c, "select * from users")
	filtered := b.conn.lastQuery()
	runTab(t, c, c.Handle(keymap.LoadMore)) // 3 rows, max_rows 2: truncated
	if b.conn.lastQuery() != filtered || c.shown.where == "" {
		t.Fatalf("load more ran %q, where %q", b.conn.lastQuery(), c.shown.where)
	}
	c.onResults = false // RunStatement is an editor action
	runSQL(t, c, "select * from users")
	if c.shown.where != "" || c.shown.base != "select * from users" {
		t.Fatalf("shown = %+v", c.shown)
	}
}

func TestConsoleFilterFailureKeepsRows(t *testing.T) {
	b := newFakeBackend()
	b.conn.setFail("select * from users\nWHERE (`id` = '1'\n)", errors.New("Duplicate column name 'id'"))
	c := newConsole(t, b, "local")
	filterFirstID(t, c, "select * from users")
	if c.res.failedWhere != "`id` = '1'" || c.shown.where != "" {
		t.Fatalf("failed %q, where %q", c.res.failedWhere, c.shown.where)
	}
	if !strings.Contains(consoleView(c), "user1") || !strings.Contains(c.status, "Duplicate column") {
		t.Fatalf("status %q:\n%s", c.status, consoleView(c))
	}
}

func TestConsoleUnsavedChangesBlockFilter(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	stageName(t, c)
	before := b.conn.lastQuery()
	runTab(t, c, c.Handle(keymap.FilterEqual))
	if b.conn.lastQuery() != before || !strings.Contains(c.status, "1 unsaved change") {
		t.Fatalf("ran %q, status %q", b.conn.lastQuery(), c.status)
	}
}

func TestConsoleFilterNeedsASelect(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	runSQL(t, c, "show tables")
	c.onResults = true
	runTab(t, c, c.Handle(keymap.FilterEqual))
	if !strings.Contains(c.status, "filter works on SELECT results") {
		t.Fatalf("status = %q", c.status)
	}
}
