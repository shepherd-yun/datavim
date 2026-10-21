// Package edit stages changes to a page of table data and turns them into
// the statements that apply them (spec §5.8).
package edit

import (
	"errors"
	"slices"

	"github.com/shepherd-yun/datavim/internal/db"
)

// State is how a row differs from the server.
type State int

const (
	Unchanged State = iota
	Modified
	Inserted
	Deleted
)

// Row is one row of the page being edited. Callers read it; only Table
// changes it.
type Row struct {
	ID     string     // row id (PostgreSQL ctid, Oracle ROWID) when rows are located by it
	Orig   []db.Value // as read from the server; nil for a new row
	Values []db.Value // as shown and written
	Set    []bool     // cells given a value (new rows: cells not left to their default)
	State  State
}

func (r *Row) clone() *Row {
	c := *r
	c.Values = slices.Clone(r.Values)
	c.Set = slices.Clone(r.Set)
	return &c
}

func (r *Row) equal(o *Row) bool {
	return r.State == o.State && slices.Equal(r.Values, o.Values) && slices.Equal(r.Set, o.Set)
}

// ErrDeleted is returned when a row marked for deletion is edited.
var ErrDeleted = errors.New("the row is marked for deletion · u undoes that")

const maxUndo = 1000

// step undoes one change: it puts before back at index at, or removes the
// row at at when before is nil (the change added it). removed means the
// change took the row out, so undoing puts it back in.
type step struct {
	at      int
	before  *Row
	removed bool
}

// Table is a page of rows with staged changes and their undo history.
type Table struct {
	cols []db.ColumnInfo
	orig [][]db.Value
	ids  []string
	rows []*Row
	undo []step
}

// New stages changes over rows; ids, when not nil, holds each row's row id.
func New(cols []db.ColumnInfo, rows [][]db.Value, ids []string) *Table {
	t := &Table{cols: cols, orig: rows, ids: ids}
	t.Reset()
	return t
}

// Reset drops every staged change and the undo history.
func (t *Table) Reset() {
	t.rows = make([]*Row, len(t.orig))
	for i, vals := range t.orig {
		r := &Row{Orig: vals, Values: slices.Clone(vals), Set: make([]bool, len(t.cols))}
		if i < len(t.ids) {
			r.ID = t.ids[i]
		}
		t.rows[i] = r
	}
	t.undo = nil
}

// Append adds rows read from the server after the current ones, keeping
// staged changes and undo; ids, when not nil, holds each row's row id.
func (t *Table) Append(rows [][]db.Value, ids []string) {
	for i, vals := range rows {
		r := &Row{Orig: vals, Values: slices.Clone(vals), Set: make([]bool, len(t.cols))}
		if i < len(ids) {
			r.ID = ids[i]
		}
		if t.ids != nil || ids != nil {
			for len(t.ids) < len(t.orig) {
				t.ids = append(t.ids, "")
			}
			t.ids = append(t.ids, r.ID)
		}
		t.orig = append(t.orig, vals)
		t.rows = append(t.rows, r)
	}
}

func (t *Table) Columns() []db.ColumnInfo { return t.cols }
func (t *Table) Len() int                 { return len(t.rows) }
func (t *Table) Row(i int) *Row           { return t.rows[i] }

// Result is the rows as shown: staged values, new rows included. Cells
// left to their default read as NULL.
func (t *Table) Result() *db.ResultSet {
	rs := &db.ResultSet{Columns: t.cols, Rows: make([][]db.Value, len(t.rows))}
	for i, r := range t.rows {
		rs.Rows[i] = r.Values
	}
	return rs
}

// Changes counts the rows with a staged change.
func (t *Table) Changes() int {
	n := 0
	for _, r := range t.rows {
		if r.State != Unchanged {
			n++
		}
	}
	return n
}

func (t *Table) push(s step) {
	t.undo = append(t.undo, s)
	if over := len(t.undo) - maxUndo; over > 0 {
		t.undo = t.undo[over:]
	}
}

func stateOf(r *Row) State {
	switch {
	case r.Orig == nil:
		return Inserted
	case slices.Contains(r.Set, true):
		return Modified
	}
	return Unchanged
}

// SetCell stages v as a cell's value. Setting a cell back to its server
// value unstages it.
func (t *Table) SetCell(row, col int, v db.Value) error {
	r := t.rows[row]
	if r.State == Deleted {
		return ErrDeleted
	}
	before := r.clone()
	r.Values[col] = v
	r.Set[col] = r.Orig == nil || v != r.Orig[col]
	r.State = stateOf(r)
	if !r.equal(before) {
		t.push(step{at: row, before: before})
	}
	return nil
}

// InsertAfter adds a row below row (-1: at the top) with every cell left
// to its default and returns its index.
func (t *Table) InsertAfter(row int) int {
	at := min(max(row+1, 0), len(t.rows))
	r := &Row{Values: make([]db.Value, len(t.cols)), Set: make([]bool, len(t.cols)), State: Inserted}
	for i := range r.Values {
		r.Values[i].Null = true
	}
	t.rows = slices.Insert(t.rows, at, r)
	t.push(step{at: at})
	return at
}

// Delete marks a row for deletion; a new row is dropped instead. A row
// already marked stays as it is.
func (t *Table) Delete(row int) {
	r := t.rows[row]
	switch r.State {
	case Deleted:
	case Inserted:
		t.push(step{at: row, before: r, removed: true})
		t.rows = slices.Delete(t.rows, row, row+1)
	default:
		t.push(step{at: row, before: r.clone()})
		r.State = Deleted
	}
}

// Undo reverts the last staged change and returns the row it touched.
func (t *Table) Undo() (int, bool) {
	if len(t.undo) == 0 {
		return 0, false
	}
	s := t.undo[len(t.undo)-1]
	t.undo = t.undo[:len(t.undo)-1]
	switch {
	case s.before == nil:
		t.rows = slices.Delete(t.rows, s.at, s.at+1)
	case s.removed:
		t.rows = slices.Insert(t.rows, s.at, s.before)
	default:
		t.rows[s.at] = s.before
	}
	return min(s.at, max(len(t.rows)-1, 0)), true
}
