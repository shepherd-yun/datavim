package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
)

// selectNode focuses the tree and selects conn/path..., loading on the way.
func selectNode(t *testing.T, m *Model, conn string, path ...string) {
	t.Helper()
	m.setFocus(focusTree)
	drain(t, m, m.reveal(conn, path))
	if n := m.tree.Selected(); n == nil || n.Name != path[len(path)-1] {
		t.Fatalf("selected %+v, want %v", n, path)
	}
}

// typeText presses each rune of s.
func typeText(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		press(t, m, string(r))
	}
}

// childNames lists the children of the node that holds conn's app tables.
func childNames(m *Model, conn string) []string {
	n := m.tree.Namespace(conn, db.Namespace{Database: "app"})
	if n == nil {
		return nil
	}
	var out []string
	for _, c := range n.Children {
		out = append(out, c.Name)
	}
	return out
}

const dropUsers = "DROP TABLE `app`.`users`"

func TestDropTableNeedsItsName(t *testing.T) {
	m, b := newTestModel(t)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "d", "d")
	s := screen(m)
	for _, want := range []string{"Drop table users", dropUsers + ";", "Type users to confirm"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	press(t, m, "y", "enter")
	if slices.Contains(b.conn.execList(), dropUsers) {
		t.Fatal("y alone dropped the table")
	}
	b.conn.setTables([]db.Table{{Name: "active", Kind: db.TableKindView}})
	press(t, m, "ctrl+u")
	typeText(t, m, "users")
	press(t, m, "enter")
	if !slices.Contains(b.conn.execList(), dropUsers) {
		t.Fatalf("execs = %v", b.conn.execList())
	}
	if !strings.Contains(statusLine(m), "dropped table users") {
		t.Fatalf("status: %s", statusLine(m))
	}
	if got := childNames(m, "local"); !slices.Equal(got, []string{"active"}) {
		t.Fatalf("tree children = %v", got)
	}
}

func TestDropView(t *testing.T) {
	m, b := newTestModel(t)
	selectNode(t, m, "local", "app", "active")
	press(t, m, "d", "d")
	if !strings.Contains(screen(m), "Drop view active") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	typeText(t, m, "active")
	press(t, m, "enter")
	if !slices.Contains(b.conn.execList(), "DROP VIEW `app`.`active`") {
		t.Fatalf("execs = %v", b.conn.execList())
	}
}

func TestDropOnProdSaysProd(t *testing.T) {
	m, b := newTestModel(t)
	if err := b.SetReadOnly("prod", false); err != nil { // prod is read-only by default
		t.Fatal(err)
	}
	selectNode(t, m, "prod", "app", "users")
	press(t, m, "d", "d")
	if !strings.Contains(screen(m), "PROD · Drop table users") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestDropClosesTabsOfTheTable(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, "ctrl+h", "d", "d")
	typeText(t, m, "users")
	press(t, m, "enter")
	if len(m.tabs) != 0 {
		t.Fatalf("tabs = %d", len(m.tabs))
	}
}

func TestDropBlockedByUnsavedChanges(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, "d", "d") // stage a row deletion in the grid
	press(t, m, "ctrl+h", "d", "d")
	if strings.Contains(screen(m), "Drop table") || !strings.Contains(statusLine(m), "unsaved change") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestDropBlockedByOpenTransaction(t *testing.T) {
	m, _ := newTestModel(t)
	openTxConsole(t, m)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "d", "d")
	if strings.Contains(screen(m), "Drop table") || !strings.Contains(statusLine(m), "open transaction") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestDropBlockedOnReadOnly(t *testing.T) {
	m, b := newTestModel(t)
	if err := b.SetReadOnly("local", true); err != nil {
		t.Fatal(err)
	}
	selectNode(t, m, "local", "app", "users")
	press(t, m, "d", "d")
	if strings.Contains(screen(m), "Drop table") || !strings.Contains(statusLine(m), "read-only") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestDropFailureKeepsTabs(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setFail(dropUsers, errors.New("cannot drop: referenced by orders"))
	openUsers(t, m)
	press(t, m, "ctrl+h", "d", "d")
	typeText(t, m, "users")
	press(t, m, "enter")
	if len(m.tabs) != 1 || !strings.Contains(statusLine(m), "referenced by orders") {
		t.Fatalf("tabs %d, status %s", len(m.tabs), statusLine(m))
	}
}

func TestDropConnLostIsNotRetried(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setFail(dropUsers, fmt.Errorf("%w: connection reset by peer", db.ErrConnLost))
	selectNode(t, m, "local", "app", "users")
	press(t, m, "d", "d")
	typeText(t, m, "users")
	press(t, m, "enter")
	n := 0
	for _, q := range b.conn.execList() {
		if q == dropUsers {
			n++
		}
	}
	if n != 1 || !strings.Contains(statusLine(m), "may or may not have run") {
		t.Fatalf("ran %d times, status %s", n, statusLine(m))
	}
}

func TestDropOnDatabaseSaysWhatCanBeDeleted(t *testing.T) {
	m, _ := newTestModel(t)
	selectNode(t, m, "local", "app")
	press(t, m, "d", "d")
	if !strings.Contains(statusLine(m), "connections, tables and views") {
		t.Fatalf("status: %s", statusLine(m))
	}
}

func TestPreparedOpKeepsAnOpenPopup(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "?")
	help := m.overlay
	op := tableOp{kind: opDrop, conn: "local", ref: db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"}}
	m.Update(tableOpPreparedMsg{op: op, sql: dropUsers})
	if help == nil || m.overlay != help || strings.Contains(screen(m), "Drop table") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestTablePath(t *testing.T) {
	if got := tablePath(db.Namespace{Database: "app"}, "users"); !slices.Equal(got, []string{"app", "users"}) {
		t.Fatal(got)
	}
	if got := tablePath(db.Namespace{Database: "app", Schema: "public"}, "users"); !slices.Equal(got, []string{"app", "public", "users"}) {
		t.Fatal(got)
	}
}

const renameUsers = "RENAME TABLE `app`.`users` TO `app`.`people`"

// renameTo opens the rename prompt on the selected table and submits name.
func renameTo(t *testing.T, m *Model, name string) {
	t.Helper()
	press(t, m, "r", "ctrl+u")
	typeText(t, m, name)
	press(t, m, "enter")
}

func TestRenameTable(t *testing.T) {
	m, b := newTestModel(t)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "r")
	if s := screen(m); !strings.Contains(s, "Rename table users") {
		t.Fatalf("screen:\n%s", s)
	}
	press(t, m, "ctrl+u")
	typeText(t, m, "people")
	press(t, m, "enter")
	if s := screen(m); !strings.Contains(s, renameUsers+";") {
		t.Fatalf("preview missing:\n%s", s)
	}
	b.conn.setTables([]db.Table{{Name: "people"}, {Name: "active", Kind: db.TableKindView}})
	press(t, m, "y")
	if !slices.Contains(b.conn.execList(), renameUsers) {
		t.Fatalf("execs = %v", b.conn.execList())
	}
	if n := m.tree.Selected(); n == nil || n.Name != "people" {
		t.Fatalf("selected = %+v", n)
	}
	if !strings.Contains(statusLine(m), "renamed table users to people") {
		t.Fatalf("status: %s", statusLine(m))
	}
}

func TestRenameRetargetsTabs(t *testing.T) {
	m, b := newTestModel(t)
	openUsers(t, m)
	press(t, m, "space", "S") // structure tab too
	b.conn.setTables([]db.Table{{Name: "people"}})
	press(t, m, "ctrl+h")
	renameTo(t, m, "people")
	press(t, m, "y")
	if len(m.tabs) != 2 {
		t.Fatalf("tabs = %d", len(m.tabs))
	}
	for _, tb := range m.tabs {
		if _, ref := tb.Target(); ref.Name != "people" || !strings.HasPrefix(tb.Title(), "people") {
			t.Fatalf("tab %q still on %s", tb.Title(), ref.Name)
		}
	}
}

func TestRenameRejectsUnchangedOrEmptyName(t *testing.T) {
	m, b := newTestModel(t)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "r", "enter")
	if !strings.Contains(screen(m), "unchanged") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	press(t, m, "ctrl+u", "enter")
	if !strings.Contains(screen(m), "enter a name") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	if len(b.conn.execList()) != 0 {
		t.Fatalf("execs = %v", b.conn.execList())
	}
}

func TestRenameOnProdNeedsName(t *testing.T) {
	m, b := newTestModel(t)
	if err := b.SetReadOnly("prod", false); err != nil { // prod is read-only by default
		t.Fatal(err)
	}
	selectNode(t, m, "prod", "app", "users")
	renameTo(t, m, "people")
	if !strings.Contains(screen(m), "PROD · Rename table users") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	press(t, m, "y", "enter")
	if slices.Contains(b.conn.execList(), renameUsers) {
		t.Fatal("y alone renamed on prod")
	}
	press(t, m, "ctrl+u")
	typeText(t, m, "users")
	press(t, m, "enter")
	if !slices.Contains(b.conn.execList(), renameUsers) {
		t.Fatalf("execs = %v", b.conn.execList())
	}
}

func TestRenameFailureKeepsTabs(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setFail(renameUsers, errors.New("table people already exists"))
	openUsers(t, m)
	press(t, m, "ctrl+h")
	renameTo(t, m, "people")
	press(t, m, "y")
	if _, ref := m.tabs[0].Target(); ref.Name != "users" || !strings.Contains(statusLine(m), "already exists") {
		t.Fatalf("tab on %s, status %s", ref.Name, statusLine(m))
	}
}

func TestRenameBlockedByUnsavedChanges(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, "d", "d")
	press(t, m, "ctrl+h", "r")
	if strings.Contains(screen(m), "Rename table") || !strings.Contains(statusLine(m), "unsaved change") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

const truncateUsers = "TRUNCATE TABLE `app`.`users`"

func TestTruncateTableNeedsItsNameAndReloadsData(t *testing.T) {
	m, b := newTestModel(t)
	openUsers(t, m)
	press(t, m, "ctrl+h")
	drain(t, m, m.dispatch(keymap.TruncateTable))
	if s := screen(m); !strings.Contains(s, "Truncate table users") || !strings.Contains(s, "Every row") {
		t.Fatalf("screen:\n%s", s)
	}
	queries := b.conn.queryCount()
	typeText(t, m, "users")
	press(t, m, "enter")
	if !slices.Contains(b.conn.execList(), truncateUsers) {
		t.Fatalf("execs = %v", b.conn.execList())
	}
	if len(m.tabs) != 1 || b.conn.queryCount() == queries {
		t.Fatal("the data tab was not reloaded")
	}
	if !strings.Contains(statusLine(m), "truncated table users") {
		t.Fatalf("status: %s", statusLine(m))
	}
}

func TestTruncateRefusesViews(t *testing.T) {
	m, b := newTestModel(t)
	selectNode(t, m, "local", "app", "active")
	drain(t, m, m.dispatch(keymap.TruncateTable))
	if !strings.Contains(statusLine(m), "only tables can be truncated") || len(b.conn.execList()) != 0 {
		t.Fatalf("status %s, execs %v", statusLine(m), b.conn.execList())
	}
}
