// Package schemaedit stages changes to one table's columns and indexes, as
// the structure tab's Modify Table makes them, and plans them as
// db.SchemaChanges (with package schemadiff) for a db.DDLWriter to write.
// It knows nothing about terminals.
package schemaedit

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// ErrDeleted is an edit of a column or index marked for deletion.
var ErrDeleted = errors.New("marked for deletion · u undoes that")

// State is what a row's staged edits do to it.
type State int

const (
	Unchanged State = iota
	Changed
	Added
	Deleted
)

// ColumnField is one editable field of a column, in the order the
// structure view shows them.
type ColumnField int

const (
	ColName ColumnField = iota
	ColType
	ColNullable
	ColDefault
	ColComment
	ColumnFields // how many there are
)

// IndexField is one editable field of an index.
type IndexField int

const (
	IdxName IndexField = iota
	IdxKind
	IdxColumns
	IndexFields
)

// ColumnRow is a column with its staged edits.
type ColumnRow struct {
	Orig    *db.Column // as read; nil for a new column
	Col     db.Column  // as it should be
	Deleted bool
}

func sameDefault(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// Changed reports whether f differs from the column as read; false for a
// new column, which is new as a whole.
func (r ColumnRow) Changed(f ColumnField) bool {
	if r.Orig == nil {
		return false
	}
	o, c := r.Orig, r.Col
	switch f {
	case ColName:
		return c.Name != o.Name
	case ColType:
		return c.Type != o.Type
	case ColNullable:
		return c.Nullable != o.Nullable
	case ColDefault:
		return !sameDefault(c.Default, o.Default)
	case ColComment:
		return c.Comment != o.Comment
	}
	return false
}

func (r ColumnRow) State() State {
	switch {
	case r.Orig == nil:
		return Added
	case r.Deleted:
		return Deleted
	}
	for f := range ColumnFields {
		if r.Changed(f) {
			return Changed
		}
	}
	return Unchanged
}

// Text is f as the structure view shows it.
func (r ColumnRow) Text(f ColumnField) string {
	switch f {
	case ColName:
		return r.Col.Name
	case ColType:
		return r.Col.Type
	case ColNullable:
		if r.Col.Nullable {
			return "NULL"
		}
		return "NOT NULL"
	case ColDefault:
		if r.Col.Default == nil {
			return ""
		}
		return *r.Col.Default
	case ColComment:
		return r.Col.Comment
	}
	return ""
}

// IndexRow is an index with its staged edits.
type IndexRow struct {
	Orig    *db.Index // as read; nil for a new index
	Index   db.Index  // as it should be
	Deleted bool

	origin   int      // position in Before().Indexes; -1 for a new index
	base     []string // Orig's columns under the staged column renames
	touched  bool     // the columns were typed, so renames leave them alone
	autoName bool     // the name was made up and follows columns and kind
}

func (r IndexRow) Changed(f IndexField) bool {
	if r.Orig == nil {
		return false
	}
	switch f {
	case IdxName:
		return r.Index.Name != r.Orig.Name
	case IdxKind:
		return r.Index.Unique != r.Orig.Unique || r.Index.Primary != r.Orig.Primary
	case IdxColumns:
		return !slices.Equal(r.Index.Columns, r.base)
	}
	return false
}

func (r IndexRow) State() State {
	switch {
	case r.Orig == nil:
		return Added
	case r.Deleted:
		return Deleted
	}
	for f := range IndexFields {
		if r.Changed(f) {
			return Changed
		}
	}
	return Unchanged
}

func (r IndexRow) Text(f IndexField) string {
	switch f {
	case IdxName:
		return r.Index.Name
	case IdxKind:
		switch {
		case r.Index.Primary:
			return "PRIMARY"
		case r.Index.Unique:
			return "UNIQUE"
		}
		return "INDEX"
	case IdxColumns:
		return strings.Join(r.Index.Columns, ", ")
	}
	return ""
}

// Pos is where an edit was made, for putting the cursor back on undo.
type Pos struct {
	Indexes bool // the Indexes section, else Columns
	Row     int
}

type snapshot struct {
	cols []ColumnRow
	idx  []IndexRow
	pos  Pos
}

// Table is one table being modified.
type Table struct {
	before  db.TableSchema
	quote   func(name string) string // a column name as index entries spell it
	cols    []ColumnRow
	idx     []IndexRow
	history []snapshot
}

// New starts modifying s, a table as MetaReader.TableSchema read it on an
// engine of dialect d. Modify Table is a schema tool: d is MySQL or
// PostgreSQL (sqlx.Features.SchemaTools), so it checks for one of the two.
func New(s db.TableSchema, d sqlx.Dialect) *Table {
	t := &Table{before: s, quote: func(n string) string { return n }}
	if d == sqlx.Postgres {
		// PostgreSQL's index entries are pg_get_indexdef text, quoted where needed.
		t.quote = func(n string) string { return sqlx.QuoteIfNeeded(n, sqlx.Postgres) }
	}
	t.Reset()
	return t
}

func (t *Table) Before() db.TableSchema { return t.before }
func (t *Table) Columns() []ColumnRow   { return t.cols }
func (t *Table) Indexes() []IndexRow    { return t.idx }

// Reset drops every staged edit and the undo history.
func (t *Table) Reset() {
	t.cols = make([]ColumnRow, len(t.before.Columns))
	for i := range t.before.Columns {
		t.cols[i] = ColumnRow{Orig: &t.before.Columns[i], Col: t.before.Columns[i]}
	}
	t.idx = make([]IndexRow, len(t.before.Indexes))
	for i := range t.before.Indexes {
		ix := t.before.Indexes[i]
		ix.Columns = slices.Clone(ix.Columns)
		t.idx[i] = IndexRow{Orig: &t.before.Indexes[i], Index: ix, origin: i, base: slices.Clone(ix.Columns)}
	}
	t.history = nil
}

// Changes counts the columns and indexes with staged edits.
func (t *Table) Changes() int {
	n := 0
	for _, r := range t.cols {
		if r.State() != Unchanged {
			n++
		}
	}
	for _, r := range t.idx {
		if r.State() != Unchanged {
			n++
		}
	}
	return n
}

// push saves the rows before an edit at pos.
func (t *Table) push(pos Pos) {
	idx := slices.Clone(t.idx)
	for i := range idx {
		idx[i].Index.Columns = slices.Clone(idx[i].Index.Columns)
		idx[i].base = slices.Clone(idx[i].base)
	}
	t.history = append(t.history, snapshot{cols: slices.Clone(t.cols), idx: idx, pos: pos})
}

// Undo takes back the last edit and says where it was made.
func (t *Table) Undo() (Pos, bool) {
	if len(t.history) == 0 {
		return Pos{}, false
	}
	s := t.history[len(t.history)-1]
	t.history = t.history[:len(t.history)-1]
	t.cols, t.idx = s.cols, s.idx
	return s.pos, true
}

func (t *Table) liveColumns() int {
	n := 0
	for _, r := range t.cols {
		if !r.Deleted {
			n++
		}
	}
	return n
}

// SetColumn sets the name, type, default or comment of column i from
// typed text. A type typed in another case than the one read is no change.
func (t *Table) SetColumn(i int, f ColumnField, text string) error {
	r := t.cols[i]
	if r.Deleted {
		return ErrDeleted
	}
	c := r.Col
	switch f {
	case ColName:
		name := strings.TrimSpace(text)
		if name == "" {
			return errors.New("a column needs a name")
		}
		c.Name = name
	case ColType:
		typ := strings.TrimSpace(text)
		switch {
		case typ == "":
			return errors.New("a column needs a type")
		case c.Generated != "":
			return fmt.Errorf("%s is a generated column: change it in a console", c.Name)
		case r.Orig != nil && strings.EqualFold(typ, r.Orig.Type):
			typ = r.Orig.Type
		}
		c.Type = typ
	case ColDefault:
		def := strings.TrimSpace(text)
		switch {
		case c.Generated != "":
			return fmt.Errorf("%s is a generated column: it has no default", c.Name)
		case c.AutoIncrement:
			return fmt.Errorf("%s is auto-increment: it has no default", c.Name)
		case def == "":
			c.Default = nil
		case r.Orig != nil && r.Orig.Default != nil && *r.Orig.Default == def:
			c.Default = r.Orig.Default
		default:
			c.Default = &def
		}
	case ColComment:
		c.Comment = text
	default:
		return fmt.Errorf("schemaedit: SetColumn cannot set field %d", f)
	}
	if (ColumnRow{Col: c}).Text(f) == r.Text(f) {
		return nil
	}
	t.push(Pos{Row: i})
	t.cols[i].Col = c
	t.syncIndexes()
	return nil
}

// ToggleNullable switches column i between NULL and NOT NULL.
func (t *Table) ToggleNullable(i int) error {
	if t.cols[i].Deleted {
		return ErrDeleted
	}
	t.push(Pos{Row: i})
	t.cols[i].Col.Nullable = !t.cols[i].Col.Nullable
	return nil
}

// AddColumn adds a nullable column with no name and type below row after
// (-1 puts it first) and returns its row.
func (t *Table) AddColumn(after int) int {
	at := after + 1
	t.push(Pos{Row: max(after, 0)})
	t.cols = slices.Insert(t.cols, at, ColumnRow{Col: db.Column{Nullable: true}})
	return at
}

// DeleteColumn drops a new column, or marks one read from the server.
func (t *Table) DeleteColumn(i int) error {
	r := t.cols[i]
	switch {
	case r.Deleted:
		return errors.New("already marked for deletion · u undoes that")
	case t.liveColumns() == 1:
		return errors.New("a table needs at least one column")
	}
	t.push(Pos{Row: i})
	if r.Orig == nil {
		t.cols = slices.Delete(t.cols, i, i+1)
	} else {
		t.cols[i].Deleted = true
	}
	t.syncIndexes()
	return nil
}

// renames maps the old names of the live columns read from the server to
// their staged names.
func (t *Table) renames() map[string]string {
	out := map[string]string{}
	for _, r := range t.cols {
		if r.Orig != nil && !r.Deleted && r.Col.Name != r.Orig.Name {
			out[r.Orig.Name] = r.Col.Name
		}
	}
	return out
}

// syncIndexes carries column renames into the indexes read from the
// server; the columns of an index whose columns were typed stay as typed.
func (t *Table) syncIndexes() {
	renames := t.renames()
	for i := range t.idx {
		r := &t.idx[i]
		if r.Orig == nil {
			continue
		}
		r.base = make([]string, len(r.Orig.Columns))
		for j, e := range r.Orig.Columns {
			r.base[j] = e
			if to, ok := renames[unquote(e)]; ok && !isExpression(e) {
				r.base[j] = t.quote(to)
			}
		}
		if !r.touched {
			r.Index.Columns = slices.Clone(r.base)
		}
	}
}

// SetIndexName names index i; the name no longer follows its columns.
func (t *Table) SetIndexName(i int, name string) error {
	if t.idx[i].Deleted {
		return ErrDeleted
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("an index needs a name")
	}
	t.push(Pos{Indexes: true, Row: i})
	t.idx[i].Index.Name, t.idx[i].autoName = name, false
	return nil
}

// SetIndexColumns sets index i's columns from a comma-separated list;
// column names are spelled the way the engine's index entries are.
func (t *Table) SetIndexColumns(i int, text string) error {
	if t.idx[i].Deleted {
		return ErrDeleted
	}
	entries := splitList(text)
	if len(entries) == 0 {
		return errors.New("an index needs at least one column")
	}
	for j, e := range entries {
		for _, r := range t.cols {
			if !r.Deleted && r.Col.Name == e {
				entries[j] = t.quote(e)
			}
		}
	}
	t.push(Pos{Indexes: true, Row: i})
	r := &t.idx[i]
	r.Index.Columns, r.touched = entries, true
	t.rename(r)
	return nil
}

// CycleIndexKind turns index i from INDEX to UNIQUE to PRIMARY (when the
// table has no other primary key) and back to INDEX.
func (t *Table) CycleIndexKind(i int) error {
	if t.idx[i].Deleted {
		return ErrDeleted
	}
	t.push(Pos{Indexes: true, Row: i})
	ix := &t.idx[i].Index
	switch {
	case ix.Primary:
		ix.Primary, ix.Unique = false, false
	case ix.Unique && !t.otherPrimary(i):
		ix.Primary = true
	case ix.Unique:
		ix.Unique = false
	default:
		ix.Unique = true
	}
	t.rename(&t.idx[i])
	return nil
}

func (t *Table) otherPrimary(i int) bool {
	for j, r := range t.idx {
		if j != i && !r.Deleted && r.Index.Primary {
			return true
		}
	}
	return false
}

// rename makes up r's name from the table, its columns and its kind, as
// long as the name was never typed: users_pkey, users_email_key,
// users_name_qty_idx.
func (t *Table) rename(r *IndexRow) {
	if !r.autoName {
		return
	}
	words := append([]string{t.before.Ref.Name}, nameWords(r.Index.Columns)...)
	switch {
	case r.Index.Primary:
		words = []string{t.before.Ref.Name, "pkey"}
	case r.Index.Unique:
		words = append(words, "key")
	default:
		words = append(words, "idx")
	}
	r.Index.Name = strings.Join(words, "_")
}

// AddIndex adds an index with no columns below row after (-1 puts it
// first) and returns its row.
func (t *Table) AddIndex(after int) int {
	at := after + 1
	t.push(Pos{Indexes: true, Row: max(after, 0)})
	t.idx = slices.Insert(t.idx, at, IndexRow{origin: -1, autoName: true})
	return at
}

// DeleteIndex drops a new index, or marks one read from the server.
func (t *Table) DeleteIndex(i int) error {
	r := t.idx[i]
	if r.Deleted {
		return errors.New("already marked for deletion · u undoes that")
	}
	t.push(Pos{Indexes: true, Row: i})
	if r.Orig == nil {
		t.idx = slices.Delete(t.idx, i, i+1)
	} else {
		t.idx[i].Deleted = true
	}
	return nil
}
