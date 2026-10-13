package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
)

// cellEditedMsg is a value saved in the cell editor.
type cellEditedMsg struct {
	tabID    int
	table    *edit.Table // the page the edit was made on
	row, col int
	value    db.Value
}

func (m cellEditedMsg) TabID() int { return m.tabID }

// plural is "1 change", "2 changes".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// changes counts the rows with staged changes.
func (t *dataTab) changes() int {
	if t.table == nil {
		return 0
	}
	return t.table.Changes()
}

// mark is how the grid draws a cell of the page.
func (t *dataTab) mark(row, col int) grid.Mark {
	if t.table == nil || row >= t.table.Len() {
		return grid.MarkNone
	}
	r := t.table.Row(row)
	switch {
	case r.State == edit.Deleted:
		return grid.MarkDeleted
	case r.State == edit.Inserted && r.Set[col]:
		return grid.MarkInserted
	case r.State == edit.Inserted:
		return grid.MarkDefault
	case r.Set[col]:
		return grid.MarkChanged
	}
	return grid.MarkNone
}

// editBlocked says why the page cannot be changed now ("" when it can);
// existing is set for changes to rows read from the server.
func (t *dataTab) editBlocked(existing bool) string {
	cfg, _ := t.backend.Connection(t.conn)
	switch {
	case t.table == nil || t.loading:
		return "wait for the rows to load"
	case cfg.IsReadOnly():
		return "read-only connection · :set noreadonly allows changes for this session"
	case existing && t.key.Kind == edit.NoKey:
		return t.ref.Name + " has no primary or unique key: rows can only be added"
	}
	return ""
}

// showChanges redraws the page after a staged change, keeping the cursor.
func (t *dataTab) showChanges() { t.grid.ReplaceResult(t.table.Result()) }

func (t *dataTab) editCell() tea.Cmd {
	if t.table == nil || t.table.Len() == 0 {
		t.setStatus("no row here · o adds one", true)
		return nil
	}
	row, col := t.grid.Position()
	r := t.table.Row(row)
	if msg := t.editBlocked(r.State != edit.Inserted); msg != "" {
		t.setStatus(msg, true)
		return nil
	}
	if r.State == edit.Deleted {
		t.setStatus(edit.ErrDeleted.Error(), true)
		return nil
	}
	c := t.table.Columns()[col]
	id, table := t.id, t.table
	return openOverlay(overlay.NewCellEditor(t.columnTitle(c), r.Values[col], func(v db.Value) tea.Msg {
		return cellEditedMsg{tabID: id, table: table, row: row, col: col, value: v}
	}))
}

// columnTitle is "name · type", the type as the catalog spells it.
func (t *dataTab) columnTitle(c db.ColumnInfo) string {
	typ := strings.ToLower(c.DatabaseType)
	for _, mc := range t.cols {
		if mc.Name == c.Name {
			typ = mc.Type
		}
	}
	if typ == "" {
		return c.Name
	}
	return c.Name + " · " + typ
}

func (t *dataTab) cellEdited(m cellEditedMsg) {
	if m.table != t.table || m.row >= t.table.Len() {
		return // the page was reloaded meanwhile
	}
	if err := t.table.SetCell(m.row, m.col, m.value); err != nil {
		t.setStatus(err.Error(), true)
		return
	}
	t.showChanges()
}

func (t *dataTab) insertRow() {
	if msg := t.editBlocked(false); msg != "" {
		t.setStatus(msg, true)
		return
	}
	row, col := t.grid.Position()
	if t.table.Len() == 0 {
		row = -1
	}
	at := t.table.InsertAfter(row)
	t.showChanges()
	t.grid.SetCursor(at, col)
}

func (t *dataTab) deleteRow() {
	if t.table == nil || t.table.Len() == 0 {
		t.setStatus("no row here", true)
		return
	}
	row, _ := t.grid.Position()
	r := t.table.Row(row)
	if msg := t.editBlocked(r.State != edit.Inserted); msg != "" {
		t.setStatus(msg, true)
		return
	}
	if r.State == edit.Deleted {
		t.setStatus("already marked for deletion · u undoes that", false)
		return
	}
	t.table.Delete(row)
	t.showChanges()
}

func (t *dataTab) undoEdit() {
	if t.table == nil {
		return
	}
	row, ok := t.table.Undo()
	if !ok {
		t.setStatus("no changes to undo", false)
		return
	}
	_, col := t.grid.Position()
	t.showChanges()
	t.grid.SetCursor(row, col)
}

func (t *dataTab) undoAll() {
	n := t.changes()
	if n == 0 {
		t.setStatus("no changes to discard", false)
		return
	}
	t.table.Reset()
	t.showChanges()
	t.setStatus("discarded "+plural(n, "change"), false)
}
