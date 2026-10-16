package ui

import (
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/rcmd"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
)

const (
	errBinaryEdit = "binary values cannot be edited here · use the console"
	errStreamEdit = "stream entries cannot be changed · delete one and add a new one"
)

// changes counts the staged rows.
func (t *keyTab) changes() int {
	if t.table == nil {
		return 0
	}
	return t.table.Changes()
}

// unsavedHint is "<space>w submits · U discards" with the keymap's keys.
func (t *keyTab) unsavedHint(submit, discard string) string {
	return joinHints(keyHint(t.keys, keymap.RedisKey, keymap.SubmitEdits, submit), keyHint(t.keys, keymap.RedisKey, keymap.UndoAllEdits, discard))
}

// unsavedNote is "2 unsaved changes · <space>w submits · U discards".
func (t *keyTab) unsavedNote() string {
	return joinHints(plural(t.changes(), "unsaved change"), t.unsavedHint("submits", "discards"))
}

// editBlocked says why changes cannot be staged now ("" when they can).
func (t *keyTab) editBlocked() string {
	cfg, _ := t.backend.Connection(t.conn)
	switch {
	case !t.loaded || t.loading && t.table == nil:
		return "wait for the key to load"
	case !t.info.Exists:
		return "key no longer exists"
	case t.submitting:
		return "submitting changes…"
	case cfg.IsReadOnly():
		return readOnlyHint
	case t.table == nil && !t.isString():
		return t.info.Type + " keys can only be changed in the console"
	}
	return ""
}

// escapedResult is a copy of rs with every cell rcmd.Escape'd.
func escapedResult(rs *db.ResultSet) *db.ResultSet {
	out := &db.ResultSet{Columns: rs.Columns, Rows: make([][]db.Value, len(rs.Rows))}
	for i, r := range rs.Rows {
		row := make([]db.Value, len(r))
		for j, v := range r {
			if v.Null {
				row[j] = v
			} else {
				row[j] = db.Value{Text: rcmd.Escape(v.Text)}
			}
		}
		out.Rows[i] = row
	}
	return out
}

// showChanges redraws the rows, keeping the cursor.
func (t *keyTab) showChanges() { t.grid.ReplaceResult(escapedResult(t.table.Result())) }

// mark is how the grid draws a cell of the rows.
func (t *keyTab) mark(row, col int) grid.Mark {
	if t.table == nil || row >= t.table.Len() {
		return grid.MarkNone
	}
	rw := t.table.Row(row)
	switch {
	case rw.State == edit.Deleted:
		return grid.MarkDeleted
	case rw.State == edit.Inserted && rw.Set[col]:
		return grid.MarkInserted
	case rw.State == edit.Inserted:
		return grid.MarkDefault
	case rw.Set[col]:
		return grid.MarkChanged
	}
	return grid.MarkNone
}

// fixedCell says why a cell cannot be edited ("" when it can).
func (t *keyTab) fixedCell(row, col int) string {
	rw := t.table.Row(row)
	name := t.table.Columns()[col].Name
	switch {
	case t.info.Type == "zset" && col == 0,
		t.info.Type == "list" && col == 0 && rw.State != edit.Inserted:
		return "the " + name + " column cannot be edited"
	case t.info.Type == "stream" && rw.State != edit.Inserted:
		return errStreamEdit
	}
	return ""
}

// editable reports whether v can go through the editor: text, not binary.
func editable(v db.Value) bool {
	return v.Null || utf8.ValidString(v.Text) && !hasControl(v.Text)
}

func (t *keyTab) editCell() tea.Cmd {
	if msg := t.editBlocked(); msg != "" {
		t.setStatus(msg, true)
		return nil
	}
	if t.table.Len() == 0 {
		t.setStatus("nothing here · "+keyHint(t.keys, keymap.RedisKey, keymap.InsertRow, "adds one"), true)
		return nil
	}
	row, col := t.grid.Position()
	rw := t.table.Row(row)
	if rw.State == edit.Deleted {
		t.setStatus(edit.ErrDeleted.Error(), true)
		return nil
	}
	if msg := t.fixedCell(row, col); msg != "" {
		t.setStatus(msg, true)
		return nil
	}
	if !editable(rw.Values[col]) {
		t.setStatus(errBinaryEdit, true)
		return nil
	}
	id, table := t.id, t.table
	return openOverlay(overlay.NewCellEditor(t.table.Columns()[col].Name, rw.Values[col], func(v db.Value) tea.Msg {
		return cellEditedMsg{tabID: id, table: table, row: row, col: col, value: v}
	}))
}

func (t *keyTab) cellEdited(m cellEditedMsg) {
	if m.table != t.table || t.table == nil || m.row >= t.table.Len() {
		return // the key was reloaded meanwhile
	}
	if err := t.table.SetCell(m.row, m.col, m.value); err != nil {
		t.setStatus(err.Error(), true)
		return
	}
	t.showChanges()
}

func (t *keyTab) insertRow() {
	if msg := t.editBlocked(); msg != "" {
		t.setStatus(msg, true)
		return
	}
	row, col := t.grid.Position()
	if t.table.Len() == 0 {
		row = -1
	}
	at := t.table.InsertAfter(row)
	switch t.info.Type {
	case "list":
		_ = t.table.SetCell(at, 0, db.Value{Text: "tail"})
	case "stream":
		_ = t.table.SetCell(at, 0, db.Value{Text: "*"})
	}
	t.showChanges()
	t.grid.SetCursor(at, col)
}

func (t *keyTab) deleteRow() {
	if msg := t.editBlocked(); msg != "" {
		t.setStatus(msg, true)
		return
	}
	if t.table.Len() == 0 {
		t.setStatus("nothing here", true)
		return
	}
	row, _ := t.grid.Position()
	if t.table.Row(row).State == edit.Deleted {
		t.setStatus("already marked for deletion · u undoes that", false)
		return
	}
	t.table.Delete(row)
	t.showChanges()
}

func (t *keyTab) undoEdit() {
	if t.table == nil {
		t.setStatus("no changes to undo", false)
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

func (t *keyTab) undoAll() {
	n := t.changes()
	if n == 0 {
		t.setStatus("no changes to discard", false)
		return
	}
	t.table.Reset()
	t.showChanges()
	t.setStatus("discarded "+plural(n, "change"), false)
}

// submit previews the staged changes (Task 5).
func (t *keyTab) submit() tea.Cmd { return nil }

// handleEdit runs the edit actions; ok is false for other actions.
func (t *keyTab) handleEdit(a keymap.Action) (cmd tea.Cmd, ok bool) {
	switch a {
	case keymap.Refresh, keymap.FilterPrompt, keymap.FilterClear:
		if t.changes() > 0 {
			t.setStatus(t.unsavedNote(), true)
			return nil, true
		}
		return nil, false
	case keymap.EditCell:
		return t.editCell(), true
	case keymap.InsertRow:
		t.insertRow()
	case keymap.DeleteRow:
		t.deleteRow()
	case keymap.UndoEdit:
		t.undoEdit()
	case keymap.UndoAllEdits:
		t.undoAll()
	case keymap.SubmitEdits:
		return t.submit(), true
	default:
		return nil, false
	}
	return nil, true
}
