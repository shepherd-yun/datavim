package schemaedit

import (
	"fmt"
	"slices"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/schemadiff"
)

// check refuses edits no ALTER can apply as staged.
func (t *Table) check() error {
	gone := map[string]bool{} // old names of the columns renamed or dropped
	for _, r := range t.cols {
		if r.Orig != nil && (r.Deleted || r.Col.Name != r.Orig.Name) {
			gone[r.Orig.Name] = true
		}
	}
	live := map[string]bool{}
	for i, r := range t.cols {
		if r.Deleted {
			continue
		}
		name := r.Col.Name
		switch {
		case name == "":
			return fmt.Errorf("column %d needs a name", i+1)
		case r.Orig != nil && name != r.Orig.Name && gone[name]:
			// PostgreSQL renames, then drops, one statement at a time: the
			// old name is still taken when this rename runs.
			return fmt.Errorf("%s cannot take the name %s of another column in the same submit · submit in two steps", r.Orig.Name, name)
		case live[name]:
			return fmt.Errorf("two columns are called %s", name)
		case r.Col.Type == "":
			return fmt.Errorf("column %s needs a type", name)
		}
		live[name] = true
	}
	names, primaries := map[string]bool{}, 0
	for i, r := range t.idx {
		if r.Deleted {
			continue
		}
		ix := r.Index
		switch {
		case len(ix.Columns) == 0:
			return fmt.Errorf("index %d needs columns", i+1)
		case ix.Name == "":
			return fmt.Errorf("index %d needs a name", i+1)
		case names[ix.Name]:
			return fmt.Errorf("two indexes are called %s", ix.Name)
		}
		names[ix.Name] = true
		if ix.Primary {
			primaries++
		}
		for _, e := range ix.Columns {
			n := unquote(e)
			switch {
			case isExpression(e), live[n]:
			case t.dropped(n):
				return fmt.Errorf("index %s uses column %s, which is marked for deletion · delete or change the index too", ix.Name, n)
			default:
				return fmt.Errorf("index %s: no column %s", ix.Name, n)
			}
		}
	}
	if primaries > 1 {
		return fmt.Errorf("a table has one primary key")
	}
	return nil
}

// dropped reports whether the column read as name is marked for deletion.
func (t *Table) dropped(name string) bool {
	for _, r := range t.cols {
		if r.Orig != nil && r.Deleted && r.Orig.Name == name {
			return true
		}
	}
	return false
}

// Plan checks the staged edits and lists the changes that apply them,
// ordered for DDLWriter.AlterTable: index drops, column renames, drops,
// adds and alters, then index adds. Columns go through
// schemadiff.DiffTable; indexes pair by the row they were edited from
// (schemadiff.DiffIndexEdits). normType is DDLWriter.NormalizeType: it
// spells typed types the way TableSchema reads them.
func (t *Table) Plan(normType func(string) string) ([]db.SchemaChange, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	before := t.before
	before.Indexes, before.ForeignKeys = nil, nil
	after := before
	after.Columns = nil
	renames := t.renames()
	for _, r := range t.cols {
		if r.Deleted {
			continue
		}
		c := r.Col
		if r.Orig == nil || r.Changed(ColType) {
			c.Type = normType(c.Type)
		}
		after.Columns = append(after.Columns, c)
	}
	columns := schemadiff.DiffTable(before, after, renames)

	// The indexes as read, under the column renames: an index that only
	// follows a renamed column is unchanged.
	base := slices.Clone(t.before.Indexes)
	var edited []db.Index
	var origin []int
	for _, r := range t.idx {
		if r.origin >= 0 {
			base[r.origin].Columns = r.base
		}
		if !r.Deleted {
			edited = append(edited, r.Index)
			origin = append(origin, r.origin)
		}
	}
	drops, adds := schemadiff.DiffIndexEdits(base, edited, origin)
	return slices.Concat(drops, columns, adds), nil
}
