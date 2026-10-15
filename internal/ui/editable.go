package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/runner"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/complete"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
)

// resultHost is the tab an editableResult belongs to: it reloads the rows
// with another filter and runs confirmed changes.
type resultHost interface {
	setStatus(s string, isErr bool)
	// busy says why the rows cannot change now ("" when they can).
	busy() string
	// filter is the filter shown, without WHERE; "" for none.
	filter() string
	// filterTitle titles the filter prompts.
	filterTitle() string
	// checkFilter validates text typed at the F prompt.
	checkFilter(s string) error
	refilter(where string) tea.Cmd
	applyEdits(stmts []string) tea.Cmd
}

// editableResult is a grid of rows that can be filtered and edited: a
// table data tab's page, or a console's result set (spec §5.8).
type editableResult struct {
	id      int // the owning tab
	backend Backend
	conn    string
	dialect sqlx.Dialect
	keys    *keymap.Keymap
	keyCtx  keymap.Context // where hints look up keys
	grid    *grid.Model
	host    resultHost

	ref      db.TableRef // the table changes are written to
	cols     []db.Column // its columns, for types in the cell editor and F completion
	key      edit.Key    // how rows are located
	mapping  []string    // the table column behind each result column; nil: the same names
	table    *edit.Table // the rows with their staged changes
	readOnly string      // why no change can be staged ("" when one can)
	addOnly  string      // why rows read from the server cannot change ("" when they can)

	submitting  bool     // a submit is running
	previewGen  int      // tells the current preview from stale ones
	pendingSQL  []string // the previewed statements, until confirmed
	retrySubmit bool     // the last submit wanted a password
	failedWhere string   // a filter the server rejected, offered again by F
}

// changes counts the rows with staged changes.
func (r *editableResult) changes() int {
	if r.table == nil {
		return 0
	}
	return r.table.Changes()
}

// mark is how the grid draws a cell of the rows.
func (r *editableResult) mark(row, col int) grid.Mark {
	if r.table == nil || row >= r.table.Len() {
		return grid.MarkNone
	}
	rw := r.table.Row(row)
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

// unsavedHint is "<space>w submits · U discards" with the keymap's keys.
func (r *editableResult) unsavedHint(submit, discard string) string {
	return joinHints(keyHint(r.keys, r.keyCtx, keymap.SubmitEdits, submit), keyHint(r.keys, r.keyCtx, keymap.UndoAllEdits, discard))
}

// unsavedNote is "2 unsaved changes · <space>w submits · U discards".
func (r *editableResult) unsavedNote() string {
	return joinHints(plural(r.changes(), "unsaved change"), r.unsavedHint("submits", "discards"))
}

// editBlocked says why the rows cannot be changed now ("" when they can);
// existing is set for changes to rows read from the server.
func (r *editableResult) editBlocked(existing bool) string {
	cfg, _ := r.backend.Connection(r.conn)
	switch {
	case r.table == nil:
		return "wait for the rows to load"
	case r.host.busy() != "":
		return r.host.busy()
	case r.submitting:
		return "submitting changes…"
	case r.readOnly != "":
		return r.readOnly
	case cfg.IsReadOnly():
		return readOnlyHint
	case existing && r.addOnly != "":
		return r.addOnly
	}
	return ""
}

// showChanges redraws the rows after a staged change, keeping the cursor.
func (r *editableResult) showChanges() { r.grid.ReplaceResult(r.table.Result()) }

func (r *editableResult) editCell() tea.Cmd {
	if r.table == nil || r.table.Len() == 0 {
		r.host.setStatus("no row here · o adds one", true)
		return nil
	}
	row, col := r.grid.Position()
	rw := r.table.Row(row)
	if msg := r.editBlocked(rw.State != edit.Inserted); msg != "" {
		r.host.setStatus(msg, true)
		return nil
	}
	if rw.State == edit.Deleted {
		r.host.setStatus(edit.ErrDeleted.Error(), true)
		return nil
	}
	c := r.table.Columns()[col]
	id, table := r.id, r.table
	return openOverlay(overlay.NewCellEditor(r.columnTitle(c), rw.Values[col], func(v db.Value) tea.Msg {
		return cellEditedMsg{tabID: id, table: table, row: row, col: col, value: v}
	}))
}

// columnTitle is "name · type", the type as the catalog spells it.
func (r *editableResult) columnTitle(c db.ColumnInfo) string {
	typ := strings.ToLower(c.DatabaseType)
	for _, mc := range r.cols {
		if mc.Name == c.Name {
			typ = mc.Type
		}
	}
	if typ == "" {
		return c.Name
	}
	return c.Name + " · " + typ
}

func (r *editableResult) cellEdited(m cellEditedMsg) {
	if m.table != r.table || m.row >= r.table.Len() {
		return // the rows were reloaded meanwhile
	}
	if err := r.table.SetCell(m.row, m.col, m.value); err != nil {
		r.host.setStatus(err.Error(), true)
		return
	}
	r.showChanges()
}

func (r *editableResult) insertRow() {
	if msg := r.editBlocked(false); msg != "" {
		r.host.setStatus(msg, true)
		return
	}
	row, col := r.grid.Position()
	if r.table.Len() == 0 {
		row = -1
	}
	at := r.table.InsertAfter(row)
	r.showChanges()
	r.grid.SetCursor(at, col)
}

func (r *editableResult) deleteRow() {
	if r.table == nil || r.table.Len() == 0 {
		r.host.setStatus("no row here", true)
		return
	}
	row, _ := r.grid.Position()
	rw := r.table.Row(row)
	if msg := r.editBlocked(rw.State != edit.Inserted); msg != "" {
		r.host.setStatus(msg, true)
		return
	}
	if rw.State == edit.Deleted {
		r.host.setStatus("already marked for deletion · u undoes that", false)
		return
	}
	r.table.Delete(row)
	r.showChanges()
}

func (r *editableResult) undoEdit() {
	if r.table == nil {
		return
	}
	row, ok := r.table.Undo()
	if !ok {
		r.host.setStatus("no changes to undo", false)
		return
	}
	_, col := r.grid.Position()
	r.showChanges()
	r.grid.SetCursor(row, col)
}

func (r *editableResult) undoAll() {
	n := r.changes()
	if n == 0 {
		r.host.setStatus("no changes to discard", false)
		return
	}
	r.table.Reset()
	r.showChanges()
	r.host.setStatus("discarded "+plural(n, "change"), false)
}

// submit previews the staged changes as SQL after the safety checks.
func (r *editableResult) submit() tea.Cmd {
	switch {
	case r.submitting:
		r.host.setStatus("submitting changes…", true)
		return nil
	case r.changes() == 0:
		r.host.setStatus("no changes to submit", false)
		return nil
	case r.host.busy() != "":
		r.host.setStatus(r.host.busy(), true)
		return nil
	}
	stmts, warnings, err := edit.Plan(r.table, edit.Target{Dialect: r.dialect, Table: r.ref, Key: r.key, Columns: r.mapping})
	if err != nil {
		r.host.setStatus(err.Error(), true)
		return nil
	}
	cfg, _ := r.backend.Connection(r.conn)
	batch := make([]sqlx.Statement, len(stmts))
	for i, s := range stmts {
		batch[i] = sqlx.Statement{Text: s}
	}
	policy := runner.Policy{ReadOnly: cfg.IsReadOnly(), Prod: cfg.Env == config.EnvProd}
	if chk := runner.Guard(batch, r.dialect, policy); chk.Verdict == runner.Reject {
		r.host.setStatus(chk.Reason, true)
		return nil
	}
	title := "Submit " + plural(r.changes(), "change") + " to " + r.ref.Name
	if policy.Prod {
		title = "PROD · " + title
	}
	var lines []string
	for _, w := range warnings {
		lines = append(lines, "! "+w)
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	for _, s := range stmts {
		lines = append(lines, s+";")
	}
	r.previewGen++
	r.pendingSQL = stmts
	return openOverlay(overlay.NewPreview(title, lines, submitConfirmedMsg{tabID: r.id, gen: r.previewGen}))
}

// filterColumn is the column under the cursor as a filter names it: its
// table column when the filter goes into the table's WHERE.
func (r *editableResult) filterColumn() (db.ColumnInfo, bool) {
	col, ok := r.grid.Column()
	if ok && r.mapping != nil {
		_, i := r.grid.Position()
		col.Name = r.mapping[i]
	}
	return col, ok
}

func (r *editableResult) addFilter(cond string) tea.Cmd {
	return r.host.refilter(andFilter(r.host.filter(), cond))
}

var filterVerbs = map[keymap.Action]string{
	keymap.FilterContains: "contains",
	keymap.FilterLike:     "LIKE",
	keymap.FilterGreater:  ">",
	keymap.FilterLess:     "<",
}

// filterValuePrompt asks for the value of fs, fl, f> or f<, starting from
// the cell under the cursor (wrapped in % for fl).
func (r *editableResult) filterValuePrompt(a keymap.Action) tea.Cmd {
	col, ok := r.filterColumn()
	if !ok {
		r.host.setStatus("no column to filter by", true)
		return nil
	}
	initial := ""
	if _, v, ok := r.grid.Cell(); ok && !v.Null {
		initial = v.Text
	}
	label := col.Name + " " + filterVerbs[a] + " …"
	if a == keymap.FilterLike {
		initial = "%" + initial + "%"
		label += "  % any text · _ one character"
	}
	id := r.id
	return openOverlay(overlay.NewPrompt(r.host.filterTitle(), label, initial,
		func(s string) tea.Msg { return filterValueMsg{tabID: id, action: a, col: col, text: s} }))
}

// filterPrompt edits the WHERE condition, starting from a filter the
// server just rejected, else the current one, else the cursor's column.
func (r *editableResult) filterPrompt() tea.Cmd {
	initial := r.host.filter()
	switch {
	case r.failedWhere != "":
		initial = r.failedWhere
	case initial == "":
		if col, ok := r.filterColumn(); ok {
			initial = sqlx.QuoteIfNeeded(col.Name, r.dialect) + " "
		}
	}
	id := r.id
	return openOverlay(overlay.NewPrompt(r.host.filterTitle(),
		"WHERE …  e.g. name LIKE '%abc%' AND age > 18 · empty shows every row", initial,
		func(s string) tea.Msg { return filterMsg{tabID: id, text: s} },
		overlay.WithCompleter(r.completeFilter),
		overlay.WithValidate(r.host.checkFilter)))
}

// completeFilter offers the table's columns, or the result's when the
// table is unknown, and condition keywords.
func (r *editableResult) completeFilter(text string, cursor int) (int, []complete.Candidate) {
	cols := r.cols
	if len(cols) == 0 && r.table != nil {
		for _, c := range r.table.Columns() {
			cols = append(cols, db.Column{Name: c.Name, Type: strings.ToLower(c.DatabaseType)})
		}
	}
	return completeCondition(r.dialect, cols, text, cursor)
}

// handle runs the filter and edit actions; ok is false for other actions.
func (r *editableResult) handle(a keymap.Action) (cmd tea.Cmd, ok bool) {
	switch a {
	case keymap.FilterPrompt, keymap.FilterEqual, keymap.FilterNotEqual, keymap.FilterContains, keymap.FilterLike,
		keymap.FilterGreater, keymap.FilterLess, keymap.FilterNull, keymap.FilterNotNull, keymap.FilterClear:
		if r.changes() > 0 {
			r.host.setStatus(r.unsavedNote(), true)
			return nil, true
		}
	}
	switch a {
	case keymap.FilterEqual, keymap.FilterNotEqual:
		col, _ := r.filterColumn()
		_, v, ok := r.grid.Cell()
		if !ok {
			r.host.setStatus("no cell to filter by", true)
			return nil, true
		}
		op := "="
		if a == keymap.FilterNotEqual {
			op = "<>"
		}
		return r.addFilter(compareFilter(r.dialect, col, op, v)), true
	case keymap.FilterNull, keymap.FilterNotNull:
		col, ok := r.filterColumn()
		if !ok {
			r.host.setStatus("no column to filter by", true)
			return nil, true
		}
		return r.addFilter(nullFilter(r.dialect, col.Name, a == keymap.FilterNotNull)), true
	case keymap.FilterContains, keymap.FilterLike, keymap.FilterGreater, keymap.FilterLess:
		return r.filterValuePrompt(a), true
	case keymap.FilterClear:
		if r.host.filter() == "" {
			return nil, true
		}
		return r.host.refilter(""), true
	case keymap.FilterPrompt:
		return r.filterPrompt(), true
	case keymap.EditCell:
		return r.editCell(), true
	case keymap.InsertRow:
		r.insertRow()
	case keymap.DeleteRow:
		r.deleteRow()
	case keymap.UndoEdit:
		r.undoEdit()
	case keymap.UndoAllEdits:
		r.undoAll()
	case keymap.SubmitEdits:
		return r.submit(), true
	default:
		return nil, false
	}
	return nil, true
}

// update handles the messages of the cell editor, the submit preview and
// the filter prompts; ok is false for other messages.
func (r *editableResult) update(msg tea.Msg) (cmd tea.Cmd, ok bool) {
	switch m := msg.(type) {
	case cellEditedMsg:
		r.cellEdited(m)
	case submitConfirmedMsg:
		if m.gen == r.previewGen && r.pendingSQL != nil && !r.submitting {
			stmts := r.pendingSQL
			r.pendingSQL = nil
			return r.host.applyEdits(stmts), true
		}
	case filterMsg:
		return r.host.refilter(normFilter(m.text)), true
	case filterValueMsg:
		if m.text == "" {
			return nil, true
		}
		switch m.action {
		case keymap.FilterContains:
			return r.addFilter(containsFilter(r.dialect, m.col.Name, m.text)), true
		case keymap.FilterLike:
			return r.addFilter(likeFilter(r.dialect, m.col.Name, m.text)), true
		case keymap.FilterGreater:
			return r.addFilter(compareFilter(r.dialect, m.col, ">", db.Value{Text: m.text})), true
		case keymap.FilterLess:
			return r.addFilter(compareFilter(r.dialect, m.col, "<", db.Value{Text: m.text})), true
		}
	default:
		return nil, false
	}
	return nil, true
}
