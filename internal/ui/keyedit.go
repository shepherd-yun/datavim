package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/edit"
	"github.com/shepherd-yun/datavim/internal/rcmd"
	"github.com/shepherd-yun/datavim/internal/rdb"
	"github.com/shepherd-yun/datavim/internal/redit"
	"github.com/shepherd-yun/datavim/internal/session"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
)

const (
	errBinaryEdit = "binary values cannot be edited here · use the console"
	errStreamEdit = "stream entries cannot be changed · delete one and add a new one"
)

// changes counts the staged rows, or 1 for a staged string.
func (t *keyTab) changes() int {
	n := 0
	if t.table != nil {
		n = t.table.Changes()
	}
	if t.staged != nil {
		n++
	}
	return n
}

// value is the string as shown: staged, or as read.
func (t *keyTab) value() string {
	if t.staged != nil {
		return *t.staged
	}
	return t.text
}

// keyValueEditedMsg is a string's value saved in the cell editor.
type keyValueEditedMsg struct {
	tabID int
	text  string
	null  bool // ctrl+n: Redis has no NULL
}

func (m keyValueEditedMsg) TabID() int { return m.tabID }

// maxInlineValue is the longest string the cell editor takes; longer
// ones, and ones with newlines, go to $EDITOR.
const maxInlineValue = 1024

// trimEditorNewline drops the newline an editor adds at the end, unless
// old ended with one.
func trimEditorNewline(old, edited string) string {
	if strings.HasSuffix(old, "\n") {
		return edited
	}
	if s, ok := strings.CutSuffix(edited, "\r\n"); ok {
		return s
	}
	s, _ := strings.CutSuffix(edited, "\n")
	return s
}

// stringBlocked says why the string's value cannot be edited ("" when it can).
func (t *keyTab) stringBlocked() string {
	switch v := t.value(); {
	case t.editBlocked() != "":
		return t.editBlocked()
	case t.truncated():
		return "only the first 1 MB was loaded · edit this value in the console"
	case !editable(db.Value{Text: v}):
		return errBinaryEdit
	}
	return ""
}

// stageValue stages v as the string's value; the value as read unstages it.
func (t *keyTab) stageValue(v string) {
	if !t.isString() {
		return
	}
	if v == t.text {
		t.staged = nil
	} else {
		t.staged = &v
	}
	t.render()
}

// editString opens the cell editor on a short one-line value, $EDITOR on
// anything else.
func (t *keyTab) editString(external bool) tea.Cmd {
	if msg := t.stringBlocked(); msg != "" {
		t.setStatus(msg, true)
		return nil
	}
	v := t.value()
	if !external && len(v) <= maxInlineValue && !strings.Contains(v, "\n") {
		id := t.id
		return openOverlay(overlay.NewCellEditor("value", db.Value{Text: v}, func(nv db.Value) tea.Msg {
			return keyValueEditedMsg{tabID: id, text: nv.Text, null: nv.Null}
		}))
	}
	t.editingAt, t.editTable = [2]int{-1, -1}, nil
	pattern := "datavim-*.txt"
	if isJSON(v) {
		pattern = "datavim-*.json"
	}
	return writeTempEdit(t.id, v, pattern)
}

// externalCell opens $EDITOR on the grid cell under the cursor.
func (t *keyTab) externalCell() tea.Cmd {
	row, col, ok := t.editableCell()
	if !ok {
		return nil
	}
	v := t.table.Row(row).Values[col]
	t.editingAt, t.editTable = [2]int{row, col}, t.table
	pattern := "datavim-*.txt"
	if isJSON(v.Text) {
		pattern = "datavim-*.json"
	}
	return writeTempEdit(t.id, v.Text, pattern)
}

// externalEdited stages what $EDITOR saved.
func (t *keyTab) externalEdited(m editTextMsg) {
	if m.err != nil {
		t.setStatus("editor: "+m.err.Error()+" · value unchanged", true)
		return
	}
	if t.editingAt[0] < 0 {
		t.stageValue(trimEditorNewline(t.value(), m.text))
		return
	}
	row, col := t.editingAt[0], t.editingAt[1]
	if t.editTable != t.table || t.table == nil || row >= t.table.Len() {
		return // the key was reloaded meanwhile
	}
	old := t.table.Row(row).Values[col]
	v := db.Value{Text: trimEditorNewline(old.Text, m.text)}
	if v == old {
		return
	}
	if err := t.table.SetCell(row, col, v); err != nil {
		t.setStatus(err.Error(), true)
		return
	}
	t.showChanges()
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

// editableCell is the cursor's cell when it can go through an editor;
// otherwise it says why not in the status.
func (t *keyTab) editableCell() (row, col int, ok bool) {
	if msg := t.editBlocked(); msg != "" {
		t.setStatus(msg, true)
		return 0, 0, false
	}
	if t.table.Len() == 0 {
		t.setStatus("nothing here · "+keyHint(t.keys, keymap.RedisKey, keymap.InsertRow, "adds one"), true)
		return 0, 0, false
	}
	row, col = t.grid.Position()
	rw := t.table.Row(row)
	msg := t.fixedCell(row, col)
	switch {
	case rw.State == edit.Deleted:
		msg = edit.ErrDeleted.Error()
	case msg == "" && !editable(rw.Values[col]):
		msg = errBinaryEdit
	}
	if msg != "" {
		t.setStatus(msg, true)
		return 0, 0, false
	}
	return row, col, true
}

func (t *keyTab) editCell() tea.Cmd {
	row, col, ok := t.editableCell()
	if !ok {
		return nil
	}
	rw := t.table.Row(row)
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
	if t.isString() && t.staged != nil {
		t.staged = nil
		t.render()
		return
	}
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
	if t.table != nil {
		t.table.Reset()
		t.showChanges()
	}
	if t.staged != nil {
		t.staged = nil
		t.render()
	}
	t.setStatus("discarded "+plural(n, "change"), false)
}

// keyAppliedMsg is the outcome of a submit.
type keyAppliedMsg struct {
	tabID int
	conn  string
	n     int
	err   error
}

func (m keyAppliedMsg) TabID() int               { return m.tabID }
func (m keyAppliedMsg) failure() (string, error) { return m.conn, m.err }

// redisKeyChangedMsg tells the tree a key was written: typ "" means it is
// gone.
type redisKeyChangedMsg struct {
	conn     string
	db       int
	key, typ string
}

// keyChanged updates the scanned keys of the key's db; a db not scanned
// has nothing to update.
func (m *Model) keyChanged(msg redisKeyChangedMsg) {
	k := redisDBKey{conn: msg.conn, db: msg.db}
	s := m.scans[k]
	if s == nil {
		return
	}
	_, had := s.keys[msg.key]
	switch {
	case msg.typ == "" && had:
		delete(s.keys, msg.key)
		if s.total > 0 {
			s.total--
		}
	case msg.typ != "" && (had || s.pattern == "" || rcmd.Match(s.pattern, msg.key)):
		s.keys[msg.key] = msg.typ
		if !had && s.total >= 0 {
			s.total++
		}
	}
	m.rebuildDB(k, s)
}

// editPolicy is what the connection allows datavim's own commands.
func (t *keyTab) editPolicy() rcmd.Policy {
	cfg, _ := t.backend.Connection(t.conn)
	return rcmd.Policy{ReadOnly: cfg.IsReadOnly(), Prod: cfg.Env == config.EnvProd, Conn: t.conn, DBSize: -1}
}

// tomb is a list placeholder no real element is likely to equal.
func tomb() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "datavim:deleted:" + hex.EncodeToString(b[:])
}

// plan turns the staged changes into a redit.Plan.
func (t *keyTab) plan() (redit.Plan, error) {
	if t.isString() {
		if t.truncated() {
			return redit.Plan{}, errors.New("only the first 1 MB was loaded · edit this value in the console")
		}
		return redit.String(t.key, t.text, *t.staged, redit.CapsFor(t.version)), nil
	}
	return redit.Build(t.table, redit.Target{Key: t.key, Type: t.info.Type, Len: t.info.Len, Tomb: tomb()})
}

// expectText is what a check wants, as the preview shows it.
func expectText(c redit.Check) string {
	switch c.Expect {
	case redit.ExpectNil:
		return "nil"
	case redit.ExpectInt:
		return strconv.FormatInt(c.Int, 10)
	case redit.ExpectScore:
		return strconv.FormatFloat(c.Score, 'g', -1, 64)
	case redit.ExpectLen:
		return plural(int(c.Int), "element")
	}
	return `"` + strings.ReplaceAll(rcmd.Escape(c.Value), `"`, `\"`) + `"`
}

// previewLines are the checks, a blank line, and the commands.
func previewLines(p redit.Plan) []string {
	var lines []string
	for _, c := range p.Checks {
		lines = append(lines, "check: "+clipRunes(rcmd.Redact(c.Args), 200)+" == "+clipRunes(expectText(c), 200))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	for _, cmd := range p.Cmds {
		lines = append(lines, clipRunes(rcmd.Redact(cmd), 200))
	}
	return lines
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// submit previews the staged changes after the safety checks.
func (t *keyTab) submit() tea.Cmd {
	switch {
	case t.submitting:
		t.setStatus("submitting changes…", true)
		return nil
	case t.changes() == 0:
		t.setStatus("no changes to submit", false)
		return nil
	}
	if msg := t.editBlocked(); msg != "" {
		t.setStatus(msg, true)
		return nil
	}
	p, err := t.plan()
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	policy := t.editPolicy()
	if chk := rcmd.GuardArgs(p.Cmds, policy); chk.Verdict == rcmd.Reject {
		t.setStatus(chk.Reason, true)
		return nil
	}
	title := "Submit " + plural(p.N, "change") + " to " + rcmd.Escape(t.key)
	if policy.Prod {
		title = "PROD · " + title
	}
	t.previewGen++
	t.pending = &p
	return openOverlay(overlay.NewPreview(title, previewLines(p), submitConfirmedMsg{tabID: t.id, gen: t.previewGen}))
}

// apply runs the confirmed plan.
func (t *keyTab) applyPlan(p redit.Plan) tea.Cmd {
	t.submitting = true
	id, b, conn, n := t.id, t.backend, t.conn, t.db
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		err := withRedis(ctx, b, conn, n, func(c rdb.Client) error { return c.Apply(ctx, p) })
		return keyAppliedMsg{tabID: id, conn: conn, n: p.N, err: err}
	}
}

func (t *keyTab) applied(m keyAppliedMsg) tea.Cmd {
	t.submitting = false
	var changed *rdb.ChangedError
	var execErr *rdb.ExecError
	switch {
	case m.err == nil:
		t.setStatus("saved "+plural(m.n, "change"), false)
	case errors.As(m.err, &changed):
		t.setStatus(m.err.Error(), true)
		return nil // the staged changes stay
	case errors.As(m.err, &execErr):
		t.setStatus(m.err.Error(), true)
	case errors.Is(m.err, session.ErrPasswordRequired):
		return nil // the password prompt reloads the key
	default:
		t.setStatus(m.err.Error(), true)
		return nil
	}
	t.notifyTree = true
	return t.load(true)
}

// handleEdit runs the edit actions; ok is false for other actions.
func (t *keyTab) handleEdit(a keymap.Action) (cmd tea.Cmd, ok bool) {
	switch a {
	case keymap.Refresh, keymap.FilterPrompt, keymap.FilterClear:
		if t.changes() > 0 {
			t.setStatus(t.unsavedNote(), true)
			return nil, true
		}
		return nil, false
	case keymap.SetTTL, keymap.RenameTable, keymap.DeleteNode:
		if !t.loaded || !t.info.Exists {
			t.setStatus(t.header(), true)
			return nil, true
		}
		req := keyOpReq{conn: t.conn, db: t.db, key: t.key, typ: t.info.Type, ttl: t.info.TTL}
		switch a {
		case keymap.SetTTL:
			req.op = keyOpTTL
		case keymap.RenameTable:
			req.op = keyOpRename
		default:
			req.op = keyOpDelete
		}
		return func() tea.Msg { return keyOpAskMsg{req: req} }, true
	case keymap.EditCell:
		if t.isString() {
			return t.editString(false), true
		}
		return t.editCell(), true
	case keymap.ExternalEdit:
		if t.isString() {
			return t.editString(true), true
		}
		return t.externalCell(), true
	case keymap.InsertRow, keymap.DeleteRow:
		if t.isString() {
			t.setStatus("a string has one value · "+keyHint(t.keys, keymap.RedisKey, keymap.EditCell, "edits it"), true)
			return nil, true
		}
	}
	switch a {
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
