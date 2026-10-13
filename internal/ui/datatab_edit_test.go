package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

// openUsersTab opens local/app/users and returns its tab.
func openUsersTab(t *testing.T) (*Model, *fakeBackend, *dataTab) {
	t.Helper()
	m, b := newTestModel(t)
	openUsers(t, m)
	d, ok := m.activeTab().(*dataTab)
	if !ok || d.table == nil {
		t.Fatalf("users not loaded:\n%s", screen(m))
	}
	return m, b, d
}

func TestEditCellStagesAChange(t *testing.T) {
	m, _, d := openUsersTab(t)
	press(t, m, "l", "i") // name of user1
	if _, ok := m.overlay.(*overlay.CellEditor); !ok {
		t.Fatalf("i opens the cell editor:\n%s", screen(m))
	}
	if s := screen(m); !strings.Contains(s, " INSERT ") || !strings.Contains(s, "name · text") {
		t.Fatalf("the status bar shows the editor's mode, the popup the column type:\n%s", s)
	}
	press(t, m, "x", "enter")
	if m.overlay != nil {
		t.Fatal("enter saves and closes")
	}
	r := d.table.Row(0)
	if r.State != edit.Modified || r.Values[1].Text != "user1x" {
		t.Fatalf("row = %+v", r)
	}
	if s := screen(m); !strings.Contains(s, "user1x") || !strings.Contains(s, "● 1 change") {
		t.Fatalf("screen:\n%s", s)
	}
	press(t, m, "enter", "ctrl+n") // enter edits too
	if v := d.table.Row(0).Values[1]; !v.Null {
		t.Fatalf("ctrl+n sets NULL: %+v", v)
	}
}

func TestInsertDeleteAndUndoRows(t *testing.T) {
	m, _, d := openUsersTab(t)
	press(t, m, "o")
	if d.table.Len() != 3 || d.table.Row(1).State != edit.Inserted {
		t.Fatalf("o adds a row below the cursor: len %d", d.table.Len())
	}
	if row, _ := d.grid.Position(); row != 1 {
		t.Fatalf("the cursor goes to the new row: %d", row)
	}
	if !strings.Contains(screen(m), "DEFAULT") {
		t.Fatalf("new cells show DEFAULT:\n%s", screen(m))
	}
	press(t, m, "i", "9", "enter")
	if r := d.table.Row(1); !r.Set[0] || r.Values[0].Text != "9" {
		t.Fatalf("new row = %+v", r)
	}
	press(t, m, "k", "d", "d")
	if d.table.Row(0).State != edit.Deleted {
		t.Fatalf("dd marks the row: %+v", d.table.Row(0))
	}
	press(t, m, "i")
	if m.overlay != nil || !strings.Contains(screen(m), "marked for deletion") {
		t.Fatalf("a deleted row cannot be edited:\n%s", screen(m))
	}
	press(t, m, "u")
	if d.table.Row(0).State != edit.Unchanged {
		t.Fatal("u undoes the delete")
	}
	press(t, m, "U")
	if d.table.Len() != 2 || d.changes() != 0 || !strings.Contains(screen(m), "discarded 1 change") {
		t.Fatalf("U discards every change:\n%s", screen(m))
	}
	press(t, m, "u")
	if !strings.Contains(screen(m), "no changes to undo") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestUnsavedChangesBlockReloads(t *testing.T) {
	m, b, d := openUsersTab(t)
	press(t, m, "l", "i", "x", "enter")
	before := b.conn.lastQuery()
	for _, keys := range [][]string{{"]"}, {"s"}, {"f"}, {"F"}, {"space", "r"}} {
		press(t, m, keys...)
		if b.conn.lastQuery() != before || m.overlay != nil {
			t.Fatalf("%v reloaded with unsaved changes", keys)
		}
		if !strings.Contains(screen(m), "1 unsaved change · <space>w submits, U discards") {
			t.Fatalf("%v:\n%s", keys, screen(m))
		}
	}
	if d.changes() != 1 {
		t.Fatal("the change is still staged")
	}
}

func TestEditBlockedOnReadOnly(t *testing.T) {
	m, _ := newTestModel(t)
	openProdUsers(t, m) // prod is read-only by default
	press(t, m, "i")
	if m.overlay != nil || !strings.Contains(screen(m), "read-only connection") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	press(t, m, "o")
	if d := m.activeTab().(*dataTab); d.changes() != 0 {
		t.Fatal("no rows are added on a read-only connection")
	}
}

func TestKeylessTables(t *testing.T) {
	b := newFakeBackend()
	b.conn.setNoKey(true)
	if err := b.SetReadOnly("prod", false); err != nil {
		t.Fatal(err)
	}
	settings := config.Settings{PageSize: 2, MaxColumnWidth: 40}
	ns := db.Namespace{Database: "app"}

	tab := newDataTab(1, b, "prod", db.TableRef{Namespace: ns, Name: "users"}, settings)
	tab.SetSize(60, 10)
	tab.Update(run(tab.Init()))
	if q := b.conn.lastQuery(); !strings.HasPrefix(q, `SELECT ctid::text, * FROM "users"`) {
		t.Fatalf("a PostgreSQL table without a key reads ctid: %q", q)
	}
	if tab.key.Kind != edit.RowID || tab.table.Row(1).ID != "(0,2)" {
		t.Fatalf("key %+v, row %+v", tab.key, tab.table.Row(1))
	}
	if v := ansi.Strip(tab.View(true)); strings.Contains(v, "ctid") || strings.Contains(v, "(0,1)") {
		t.Fatalf("ctid is not shown:\n%s", v)
	}

	view := newDataTab(2, b, "prod", db.TableRef{Namespace: ns, Name: "active"}, settings)
	view.SetSize(60, 10)
	view.Update(run(view.Init()))
	if view.key.Kind != edit.NoKey || strings.HasPrefix(b.conn.lastQuery(), "SELECT ctid") {
		t.Fatalf("a PostgreSQL view has no ctid: %+v %q", view.key, b.conn.lastQuery())
	}
	if view.Handle(keymap.EditCell) != nil || !strings.Contains(ansi.Strip(view.View(true)), "rows can only be added") {
		t.Fatalf("view:\n%s", ansi.Strip(view.View(true)))
	}
	view.Handle(keymap.InsertRow)
	if view.changes() != 1 {
		t.Fatal("rows can still be added")
	}
}
