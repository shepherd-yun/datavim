package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/edit"
	"github.com/shepherd-yun/datavim/internal/history"
	"github.com/shepherd-yun/datavim/internal/schemadiff"
	"github.com/shepherd-yun/datavim/internal/schemaedit"
	"github.com/shepherd-yun/datavim/internal/session"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/structure"
)

const readOnlyHint = "read-only connection · :set noreadonly allows changes for this session"

var errViewNotModifiable = errors.New("views cannot be modified · change the view in a console")

type (
	// schemaLoadedMsg carries the table as Modify Table edits it, and the
	// connection's DDL writer.
	schemaLoadedMsg struct {
		tabID  int
		conn   string
		schema db.TableSchema
		ddl    db.DDLWriter
		err    error
	}
	// structEditedMsg is a field typed at a Modify Table prompt.
	structEditedMsg struct {
		tabID int
		ed    *schemaedit.Table // the edit the prompt was opened on
		pos   schemaedit.Pos
		field int // a schemaedit.ColumnField or IndexField
		text  string
	}
)

func (m schemaLoadedMsg) TabID() int { return m.tabID }
func (m structEditedMsg) TabID() int { return m.tabID }

func (t *structureTab) setStatus(s string, isErr bool) { t.status, t.statusErr = s, isErr }

func (t *structureTab) changes() int {
	if t.ed == nil {
		return 0
	}
	return t.ed.Changes()
}

// unsavedHint is "<space>w submits · U discards" with the keymap's keys.
func (t *structureTab) unsavedHint(submit, discard string) string {
	return joinHints(keyHint(t.keys, keymap.Structure, keymap.SubmitEdits, submit), keyHint(t.keys, keymap.Structure, keymap.UndoAllEdits, discard))
}

// startEdit reads the table for Modify Table, unless it is being modified
// or read already.
func (t *structureTab) startEdit() tea.Cmd {
	if t.ed != nil || t.editLoading {
		return nil
	}
	if p := schemaToolsProblem(t.backend, t.conn); p != "" {
		t.setStatus(p, true)
		return nil
	}
	if cfg, _ := t.backend.Connection(t.conn); cfg.IsReadOnly() {
		t.setStatus(readOnlyHint, true)
		return nil
	}
	t.editLoading = true
	id, b, conn, ref := t.id, t.backend, t.conn, t.ref
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := schemaLoadedMsg{tabID: id, conn: conn}
		msg.err = withConn(ctx, b, conn, ref.Database, func(c db.Conn) error {
			tables, err := c.Meta().Tables(ctx, ref.Namespace)
			if err != nil {
				return err
			}
			for _, tb := range tables {
				if tb.Name == ref.Name && tb.Kind == db.TableKindView {
					return errViewNotModifiable
				}
			}
			msg.ddl = c.Dialect().DDL()
			msg.schema, err = c.Meta().TableSchema(ctx, ref)
			if errors.Is(err, db.ErrNotFound) {
				return fmt.Errorf("table %s not found", ref.Name)
			}
			return err
		})
		return msg
	}
}

func (t *structureTab) schemaLoaded(m schemaLoadedMsg) {
	t.editLoading = false
	if m.err != nil {
		t.retryEdit = errors.Is(m.err, session.ErrPasswordRequired)
		t.setStatus(errorText(m.err), true)
		t.focus = nil
		return
	}
	t.ed, t.ddl = schemaedit.New(m.schema, t.dialect), m.ddl
	t.view.SetEdit(t.ed)
	if s := t.view.Section(); s != structure.Columns && s != structure.Indexes {
		t.view.SetCursor(structure.Columns, 0)
	}
	t.setStatus("", false)
	t.applyFocus()
}

// stopEdit goes back to the table as read, dropping any staged edits.
func (t *structureTab) stopEdit() {
	t.ed, t.ddl = nil, nil
	t.view.SetEdit(nil)
}

// toggleEdit is e in the structure tab.
func (t *structureTab) toggleEdit() tea.Cmd {
	switch n := t.changes(); {
	case t.ed == nil:
		return t.startEdit()
	case n > 0:
		t.setStatus(joinHints(plural(n, "unsaved change"), t.unsavedHint("submits", "discards")), true)
	default:
		t.stopEdit()
	}
	return nil
}

// refresh reloads the structure, and the table being modified, unless
// edits are staged.
func (t *structureTab) refresh() tea.Cmd {
	if n := t.changes(); n > 0 {
		t.setStatus(joinHints(plural(n, "unsaved change"), t.unsavedHint("submits", "discards")), true)
		return nil
	}
	if t.loading {
		return nil
	}
	cmds := []tea.Cmd{t.load()}
	if t.ed != nil {
		t.stopEdit()
		cmds = append(cmds, t.startEdit())
	}
	return tea.Batch(cmds...)
}

// editAction handles the keys that stage edits.
func (t *structureTab) editAction(a keymap.Action) tea.Cmd {
	if t.ed == nil {
		hint := keyHint(t.keys, keymap.Structure, keymap.ModifyTable, "modifies the table")
		if hint == "" {
			hint = "the table is not being modified"
		}
		t.setStatus(hint, true)
		return nil
	}
	if t.submitting {
		t.setStatus("submitting changes…", true)
		return nil
	}
	switch a {
	case keymap.UndoEdit:
		t.undo()
		return nil
	case keymap.UndoAllEdits:
		t.discard()
		return nil
	}
	sec := t.view.Section()
	if sec != structure.Columns && sec != structure.Indexes {
		t.setStatus("only columns and indexes can be changed here", true)
		return nil
	}
	row, field := t.view.Cursor()
	switch a {
	case keymap.EditCell:
		return t.editField(sec == structure.Indexes, row, field)
	case keymap.InsertRow:
		return t.insertRow(sec == structure.Indexes, row)
	case keymap.DeleteRow:
		t.deleteRow(sec == structure.Indexes, row)
	}
	return nil
}

func (t *structureTab) report(err error) {
	if err != nil {
		t.setStatus(err.Error(), true)
	}
}

// prompt asks for a field's new text.
func (t *structureTab) prompt(pos schemaedit.Pos, field int, label, initial string) tea.Cmd {
	id, ed := t.id, t.ed
	return openOverlay(overlay.NewPrompt("Modify "+t.ref.Name, label, initial, func(s string) tea.Msg {
		return structEditedMsg{tabID: id, ed: ed, pos: pos, field: field, text: s}
	}))
}

const (
	labelColumnName  = "Column name:"
	labelColumnType  = "Type, e.g. varchar(255), bigint, decimal(10,2):"
	labelDefault     = "Default as SQL ('text', 0, CURRENT_TIMESTAMP) · empty: no default:"
	labelComment     = "Comment:"
	labelIndexName   = "Index name:"
	labelIndexColumn = "Columns, comma-separated, in order:"
)

func (t *structureTab) editField(index bool, row, field int) tea.Cmd {
	add := keyHint(t.keys, keymap.Structure, keymap.InsertRow, "adds one")
	if index {
		rows := t.ed.Indexes()
		switch {
		case row >= len(rows):
			t.setStatus(joinHints("no index here", add), true)
			return nil
		case rows[row].Deleted:
			t.setStatus(schemaedit.ErrDeleted.Error(), true)
			return nil
		}
		pos := schemaedit.Pos{Indexes: true, Row: row}
		switch f := schemaedit.IndexField(field); f {
		case schemaedit.IdxKind:
			t.report(t.ed.CycleIndexKind(row))
		case schemaedit.IdxName:
			return t.prompt(pos, field, labelIndexName, rows[row].Text(f))
		case schemaedit.IdxColumns:
			return t.prompt(pos, field, labelIndexColumn, rows[row].Text(f))
		}
		return nil
	}
	rows := t.ed.Columns()
	switch {
	case row >= len(rows):
		t.setStatus(joinHints("no column here", add), true)
		return nil
	case rows[row].Deleted:
		t.setStatus(schemaedit.ErrDeleted.Error(), true)
		return nil
	}
	pos := schemaedit.Pos{Row: row}
	labels := map[schemaedit.ColumnField]string{
		schemaedit.ColName: labelColumnName, schemaedit.ColType: labelColumnType,
		schemaedit.ColDefault: labelDefault, schemaedit.ColComment: labelComment,
	}
	f := schemaedit.ColumnField(field)
	if f == schemaedit.ColNullable {
		t.report(t.ed.ToggleNullable(row))
		return nil
	}
	return t.prompt(pos, field, labels[f], rows[row].Text(f))
}

// structEdited applies a typed field. Naming a new column goes on to ask
// for its type.
func (t *structureTab) structEdited(m structEditedMsg) tea.Cmd {
	if m.ed != t.ed {
		return nil // stopped or reloaded meanwhile
	}
	if m.pos.Indexes {
		if m.pos.Row >= len(t.ed.Indexes()) {
			return nil
		}
		switch schemaedit.IndexField(m.field) {
		case schemaedit.IdxName:
			t.report(t.ed.SetIndexName(m.pos.Row, m.text))
		case schemaedit.IdxColumns:
			t.report(t.ed.SetIndexColumns(m.pos.Row, m.text))
		}
		return nil
	}
	if m.pos.Row >= len(t.ed.Columns()) {
		return nil
	}
	f := schemaedit.ColumnField(m.field)
	if err := t.ed.SetColumn(m.pos.Row, f, m.text); err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	if r := t.ed.Columns()[m.pos.Row]; f == schemaedit.ColName && r.State() == schemaedit.Added && r.Col.Type == "" {
		t.view.SetField(int(schemaedit.ColType))
		return t.prompt(m.pos, int(schemaedit.ColType), labelColumnType, "")
	}
	return nil
}

// insertRow adds a column (asking its name) or an index (asking its
// columns) below row.
func (t *structureTab) insertRow(index bool, row int) tea.Cmd {
	if index {
		after := row
		if len(t.ed.Indexes()) == 0 {
			after = -1
		}
		at := t.ed.AddIndex(after)
		t.view.SetCursor(structure.Indexes, at)
		t.view.SetField(int(schemaedit.IdxColumns))
		return t.prompt(schemaedit.Pos{Indexes: true, Row: at}, int(schemaedit.IdxColumns), labelIndexColumn, "")
	}
	after := row
	if len(t.ed.Columns()) == 0 {
		after = -1
	}
	at := t.ed.AddColumn(after)
	t.view.SetCursor(structure.Columns, at)
	t.view.SetField(int(schemaedit.ColName))
	return t.prompt(schemaedit.Pos{Row: at}, int(schemaedit.ColName), labelColumnName, "")
}

func (t *structureTab) deleteRow(index bool, row int) {
	var err error
	switch {
	case index && row < len(t.ed.Indexes()):
		err = t.ed.DeleteIndex(row)
		t.view.SetCursor(structure.Indexes, row)
	case !index && row < len(t.ed.Columns()):
		err = t.ed.DeleteColumn(row)
		t.view.SetCursor(structure.Columns, row)
	default:
		err = errors.New("nothing here to delete")
	}
	t.report(err)
}

func (t *structureTab) undo() {
	pos, ok := t.ed.Undo()
	if !ok {
		t.setStatus("no changes to undo", false)
		return
	}
	sec := structure.Columns
	if pos.Indexes {
		sec = structure.Indexes
	}
	t.view.SetCursor(sec, pos.Row)
}

func (t *structureTab) discard() {
	n := t.changes()
	if n == 0 {
		t.setStatus("no changes to discard", false)
		return
	}
	t.ed.Reset()
	t.view.SetCursor(t.view.Section(), 0)
	t.setStatus("discarded "+plural(n, "change"), false)
}

type (
	// modifySubmitMsg asks the root model to check a Modify Table submit
	// and preview it.
	modifySubmitMsg struct {
		tabID, gen int
		conn       string
		ref        db.TableRef
		stmts      []string
		warnings   []string // the destructive and lossy changes, one line each
		changes    int
	}
	// modifyConfirmedMsg is the go-ahead for the previewed statements.
	modifyConfirmedMsg struct{ tabID, gen int }
	// modifyAppliedMsg is the outcome of a Modify Table submit.
	modifyAppliedMsg struct {
		tabID int
		conn  string
		ref   db.TableRef
		n     int // statements
		err   error
	}
)

func (m modifyConfirmedMsg) TabID() int { return m.tabID }
func (m modifyAppliedMsg) TabID() int   { return m.tabID }

// submit plans the staged edits, has the connection's dialect write them
// and asks the root model to preview them. Edits the dialect cannot write
// stop it before anything runs.
func (t *structureTab) submit() tea.Cmd {
	switch {
	case t.ed == nil:
		t.setStatus("nothing to submit: the table is not being modified", true)
		return nil
	case t.submitting:
		t.setStatus("submitting changes…", true)
		return nil
	case t.changes() == 0:
		t.setStatus("no changes to submit", false)
		return nil
	}
	changes, err := t.ed.Plan(t.ddl.NormalizeType)
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	if len(changes) == 0 {
		t.setStatus("no changes to submit", false)
		return nil
	}
	stmts, manual := t.ddl.AlterTable(t.ref, changes)
	if len(manual) > 0 {
		t.setStatus("datavim cannot write "+schemadiff.Describe(manual[0])+" · undo it, or make that change in a console", true)
		return nil
	}
	var warnings []string
	for _, c := range changes {
		switch {
		case c.Risk&db.Destructive != 0:
			warnings = append(warnings, "⚠ destructive  "+schemadiff.Describe(c))
		case c.Risk&db.Lossy != 0:
			warnings = append(warnings, "⚠ lossy  "+schemadiff.Describe(c))
		}
	}
	t.gen++
	t.pendingSQL = stmts
	msg := modifySubmitMsg{tabID: t.id, gen: t.gen, conn: t.conn, ref: t.ref, stmts: stmts, warnings: warnings, changes: t.changes()}
	return func() tea.Msg { return msg }
}

// apply runs the confirmed statements on a session of their own (never a
// console's): PostgreSQL in one transaction, MySQL one by one. DDL on a
// big table can run for minutes, so there is no timeout; Ctrl-c cancels.
// It never retries: a dropped connection may or may not have run them.
func (t *structureTab) apply(stmts []string) tea.Cmd {
	t.submitting = true
	t.setStatus(joinHints("modifying "+t.ref.Name+"…", keyHint(t.keys, keymap.Global, keymap.Cancel, "cancels")), false)
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	id, b, st, conn, ref, d := t.id, t.backend, t.store, t.conn, t.ref, t.dialect
	return func() tea.Msg {
		defer cancel()
		msg := modifyAppliedMsg{tabID: id, conn: conn, ref: ref, n: len(stmts)}
		c, err := b.Conn(ctx, conn, ref.Database)
		if err == nil {
			var s db.Session
			if s, err = c.Session(ctx); err == nil {
				start := time.Now()
				err = edit.ApplyDDL(ctx, s, d, stmts)
				_ = s.Close()
				entry := history.Entry{
					Time: start, Conn: conn, Database: ref.Database,
					SQL: strings.Join(stmts, ";\n") + ";", Duration: time.Since(start),
				}
				if err != nil {
					entry.Err = errorText(err)
				}
				if herr := st.AddHistory(entry); herr != nil {
					slog.Warn("save history", "err", herr)
				}
			}
		}
		if db.IsConnLost(err) {
			b.Reset(conn)
		}
		msg.err = err
		return msg
	}
}

// cancelRun is Ctrl-c: it stops a running submit.
func (t *structureTab) cancelRun() tea.Cmd {
	if t.submitting && t.cancel != nil {
		t.cancel()
		return nil
	}
	t.setStatus("nothing is running", false)
	return nil
}

// applied stops modifying and reloads after a submit, or keeps the edits
// staged when it failed.
func (t *structureTab) applied(m modifyAppliedMsg) tea.Cmd {
	t.submitting, t.cancel = false, nil
	switch {
	case errors.Is(m.err, context.Canceled):
		t.setStatus("cancelled · changes kept", true)
		return nil
	case m.err != nil:
		t.retrySubmit = errors.Is(m.err, session.ErrPasswordRequired)
		// "changes kept" first: the tab's status line is narrow.
		t.setStatus("✗ changes kept · "+errorText(m.err), true)
		return nil
	}
	t.stopEdit()
	t.setStatus("modified "+t.ref.Name+" · "+plural(m.n, "statement"), false)
	return t.load()
}
