package datadiff

import (
	"fmt"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/edit"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// Script writes the statements that make the target's picked rows like the
// source's: DELETE for Removed, UPDATE of the differing columns for
// Changed, INSERT of every compared column for Added. Package edit writes
// them, deletes first, each UPDATE and DELETE located by the key. cols are
// the target query's result columns, Spec.Columns in order.
func (s Spec) Script(d sqlx.Dialect, target db.TableRef, cols []db.ColumnInfo, rows []Row, picked func(i int) bool) ([]string, error) {
	if len(cols) != len(s.Columns) {
		return nil, fmt.Errorf("the target returned %d columns, %d are compared", len(cols), len(s.Columns))
	}
	for i, c := range cols {
		if c.Name != s.Columns[i] {
			return nil, fmt.Errorf("the target returned column %s where %s was expected", c.Name, s.Columns[i])
		}
	}
	var orig [][]db.Value
	var at []int // the rows behind orig
	for i, r := range rows {
		if picked(i) && r.Status != Added {
			orig = append(orig, r.Target)
			at = append(at, i)
		}
	}
	t := edit.New(cols, orig, nil)
	for j, i := range at {
		r := rows[i]
		if r.Status == Removed {
			t.Delete(j)
			continue
		}
		for c := range s.Columns {
			if r.Differs(c) {
				if err := t.SetCell(j, c, r.Source[c]); err != nil {
					return nil, err
				}
			}
		}
	}
	for i, r := range rows {
		if !picked(i) || r.Status != Added {
			continue
		}
		j := t.InsertAfter(t.Len() - 1)
		for c, val := range r.Source {
			if err := t.SetCell(j, c, val); err != nil {
				return nil, err
			}
		}
	}
	stmts, _, err := edit.Plan(t, edit.Target{Dialect: d, Table: target, Key: edit.Key{Kind: edit.PrimaryKey, Columns: s.Key}})
	return stmts, err
}
