// Package datadiff compares the rows of two tables by their key: the rows
// only the source has, the rows only the target has and the rows whose
// values differ, and writes the statements that make the target's rows
// like the source's. It has no terminal or server code.
package datadiff

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// ErrNoKey means the rows of the two tables cannot be paired.
var ErrNoKey = errors.New("rows cannot be paired")

// Spec is what a comparison reads: the columns both tables have and the
// key that pairs their rows.
type Spec struct {
	Columns    []string // compared, in the source's order; the key is among them
	Key        []string
	SourceOnly []string // columns only the source has
	TargetOnly []string // columns only the target has
}

// NewSpec decides how src and tgt compare. The key is the source's primary
// key, else its first unique index over NOT NULL columns (the rule of
// edit.ChooseKey), and the target must have the same columns as one of its
// keys. Generated columns are left out: they follow from the others and
// cannot be written.
func NewSpec(src, tgt db.TableSchema) (Spec, error) {
	sk := keys(src)
	if len(sk) == 0 {
		return Spec{}, fmt.Errorf("%s has no primary key or unique NOT NULL index: %w", src.Ref.Name, ErrNoKey)
	}
	key := sk[0]
	if !slices.ContainsFunc(keys(tgt), func(k []string) bool { return slices.Equal(k, key) }) {
		return Spec{}, fmt.Errorf("%s has no primary key or unique NOT NULL index on (%s), the key of %s: %w",
			tgt.Ref.Name, strings.Join(key, ", "), src.Ref.Name, ErrNoKey)
	}
	s := Spec{Key: slices.Clone(key)}
	tcols := make(map[string]db.Column, len(tgt.Columns))
	for _, c := range tgt.Columns {
		tcols[c.Name] = c
	}
	inSource := make(map[string]bool, len(src.Columns))
	for _, c := range src.Columns {
		inSource[c.Name] = true
		tc, ok := tcols[c.Name]
		switch {
		case !ok:
			s.SourceOnly = append(s.SourceOnly, c.Name)
		case (c.Generated != "" || tc.Generated != "") && !slices.Contains(key, c.Name):
			// follows from the other columns and cannot be written
		default:
			s.Columns = append(s.Columns, c.Name)
		}
	}
	for _, c := range tgt.Columns {
		if !inSource[c.Name] {
			s.TargetOnly = append(s.TargetOnly, c.Name)
		}
	}
	return s, nil
}

// keys lists the column lists that locate one row of s: its primary key,
// then each unique, non-partial index over NOT NULL columns.
func keys(s db.TableSchema) [][]string {
	nullable := make(map[string]bool, len(s.Columns))
	var pk []string
	for _, c := range s.Columns {
		nullable[c.Name] = c.Nullable
		if c.PrimaryKey {
			pk = append(pk, c.Name)
		}
	}
	for _, ix := range s.Indexes {
		if ix.Primary && len(ix.Columns) > 0 {
			pk = ix.Columns
		}
	}
	var out [][]string
	if len(pk) > 0 {
		out = append(out, pk)
	}
	for _, ix := range s.Indexes {
		if ix.Primary || !ix.Unique || ix.Where != "" || !notNull(ix.Columns, nullable) {
			continue
		}
		out = append(out, ix.Columns)
	}
	return out
}

// notNull reports names that are all known NOT NULL columns.
func notNull(names []string, nullable map[string]bool) bool {
	if len(names) == 0 {
		return false
	}
	for _, n := range names {
		if null, known := nullable[n]; !known || null {
			return false
		}
	}
	return true
}

// Query selects the compared columns of t, narrowed by where (a condition
// without WHERE; "" reads every row). The condition is closed on its own
// line, so a trailing -- comment cannot swallow the parenthesis.
func (s Spec) Query(d sqlx.Dialect, t db.TableRef, where string) string {
	cols := make([]string, len(s.Columns))
	for i, c := range s.Columns {
		cols[i] = sqlx.QuoteIdent(d, c)
	}
	q := "SELECT " + strings.Join(cols, ", ") + " FROM " + sqlx.QualifiedName(d, t)
	if where != "" {
		q += " WHERE (" + where + "\n)"
	}
	return q
}

// KeyIndexes are the positions of the key's columns in Columns.
func (s Spec) KeyIndexes() []int {
	out := make([]int, len(s.Key))
	for i, k := range s.Key {
		out[i] = slices.Index(s.Columns, k)
	}
	return out
}

// Swap is the spec with the source and the target exchanged.
func (s Spec) Swap() Spec {
	s.SourceOnly, s.TargetOnly = s.TargetOnly, s.SourceOnly
	return s
}
