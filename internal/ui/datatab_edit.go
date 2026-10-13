package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/runner"
	"github.com/shepherd-yun/sqlvim/internal/session"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
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
	case t.submitting:
		return "submitting changes…"
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

type (
	// submitConfirmedMsg is y in the preview.
	submitConfirmedMsg struct{ tabID, gen int }
	// editsAppliedMsg is the outcome of a submit.
	editsAppliedMsg struct {
		tabID int
		conn  string
		n     int // statements
		err   error
	}
)

func (m submitConfirmedMsg) TabID() int { return m.tabID }
func (m editsAppliedMsg) TabID() int    { return m.tabID }

// submit previews the staged changes as SQL after the safety checks.
func (t *dataTab) submit() tea.Cmd {
	switch {
	case t.submitting:
		t.setStatus("submitting changes…", true)
		return nil
	case t.changes() == 0:
		t.setStatus("no changes to submit", false)
		return nil
	}
	stmts, warnings, err := edit.Plan(t.table, edit.Target{Dialect: t.dialect, Table: t.ref, Key: t.key})
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	cfg, _ := t.backend.Connection(t.conn)
	batch := make([]sqlx.Statement, len(stmts))
	for i, s := range stmts {
		batch[i] = sqlx.Statement{Text: s}
	}
	policy := runner.Policy{ReadOnly: cfg.IsReadOnly(), Prod: cfg.Env == config.EnvProd}
	if chk := runner.Guard(batch, t.dialect, policy); chk.Verdict == runner.Reject {
		t.setStatus(chk.Reason, true)
		return nil
	}
	title := "Submit " + plural(t.changes(), "change") + " to " + t.ref.Name
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
	t.gen++
	t.pendingSQL = stmts
	return openOverlay(overlay.NewPreview(title, lines, submitConfirmedMsg{tabID: t.id, gen: t.gen}))
}

// apply runs the confirmed statements in one transaction on a session of
// their own. It never retries: a dropped connection may or may not have
// committed.
func (t *dataTab) apply(stmts []string) tea.Cmd {
	t.submitting = true
	t.setStatus("submitting…", false)
	id, b, st, conn, ref, d := t.id, t.backend, t.store, t.conn, t.ref, t.dialect
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := editsAppliedMsg{tabID: id, conn: conn, n: len(stmts)}
		c, err := b.Conn(ctx, conn, ref.Database)
		if err != nil {
			msg.err = err
			return msg
		}
		s, err := c.Session(ctx)
		if err != nil {
			if db.IsConnLost(err) {
				b.Reset(conn)
			}
			msg.err = err
			return msg
		}
		start := time.Now()
		msg.err = edit.Apply(ctx, s, d, stmts)
		_ = s.Close()
		if db.IsConnLost(msg.err) {
			b.Reset(conn)
		}
		entry := history.Entry{
			Time: start, Conn: conn, Database: ref.Database,
			SQL: strings.Join(stmts, ";\n") + ";", Duration: time.Since(start),
		}
		if msg.err != nil {
			entry.Err = errorText(msg.err)
		}
		if err := st.AddHistory(entry); err != nil {
			slog.Warn("save history", "err", err)
		}
		return msg
	}
}

// applied reloads the page after a submit, or keeps the changes staged
// when it failed. The staged changes are dropped before reloading so a
// failed reload can never submit them twice.
func (t *dataTab) applied(m editsAppliedMsg) tea.Cmd {
	t.submitting = false
	if m.err != nil {
		t.retrySubmit = errors.Is(m.err, session.ErrPasswordRequired)
		// "changes kept" first: the tab's status line is narrow.
		t.setStatus("✗ changes kept · "+errorText(m.err), true)
		return nil
	}
	t.table.Reset()
	t.showChanges()
	t.setStatus("submitted "+plural(m.n, "change"), false)
	return t.load(t.page, t.query)
}
