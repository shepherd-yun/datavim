package ui

import (
	"cmp"
	"slices"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

// tableChildren builds what the tree shows under a table or view:
// Columns, Keys (primary and unique), Foreign Keys and Indexes, leaving
// out empty groups. Columns starts expanded.
func tableChildren(conn string, s db.TableSchema) []*tree.Node {
	ns := s.Ref.Namespace
	leaf := func(kind tree.Kind, name, detail string) *tree.Node {
		return &tree.Node{Kind: kind, Name: name, Detail: detail, Conn: conn, NS: ns}
	}
	var groups []*tree.Node
	add := func(g tree.Group, name string, children []*tree.Node) {
		if len(children) == 0 {
			return
		}
		n := &tree.Node{Kind: tree.KindGroup, Group: g, Name: name, Conn: conn, NS: ns, Expanded: g == tree.GroupColumns}
		tree.Adopt(n, children)
		groups = append(groups, n)
	}

	cols := slices.Clone(s.Columns)
	slices.SortStableFunc(cols, func(a, b db.Column) int { return cmp.Compare(a.Position, b.Position) })
	var columns []*tree.Node
	for _, c := range cols {
		detail := c.Type
		if c.PrimaryKey {
			detail += " PK"
		}
		columns = append(columns, leaf(tree.KindColumn, c.Name, detail))
	}
	add(tree.GroupColumns, "Columns", columns)

	var keys []*tree.Node
	for _, primary := range []bool{true, false} {
		for _, ix := range s.Indexes {
			if ix.Primary == primary && (ix.Primary || ix.Unique) {
				keys = append(keys, leaf(tree.KindKey, ix.Name, columnList(ix.Columns)))
			}
		}
	}
	add(tree.GroupKeys, "Keys", keys)

	var fks []*tree.Node
	for _, fk := range s.ForeignKeys {
		n := leaf(tree.KindForeignKey, fk.Name, columnList(fk.Columns)+" → "+refName(ns, fk.RefTable)+columnList(fk.RefColumns))
		n.Ref = fk.RefTable
		fks = append(fks, n)
	}
	add(tree.GroupForeignKeys, "Foreign Keys", fks)

	var indexes []*tree.Node
	for _, ix := range s.Indexes {
		detail := columnList(ix.Columns)
		if ix.Unique {
			detail += " unique"
		}
		if ix.Method != "" && ix.Method != "btree" {
			detail += " " + ix.Method
		}
		if ix.Where != "" {
			detail += " where " + ix.Where
		}
		indexes = append(indexes, leaf(tree.KindIndex, ix.Name, detail))
	}
	add(tree.GroupIndexes, "Indexes", indexes)
	return groups
}

func columnList(cols []string) string { return "(" + strings.Join(cols, ", ") + ")" }

// refName is a referenced table's name, qualified when it lives in
// another database or schema than ns.
func refName(ns db.Namespace, ref db.TableRef) string {
	if ref.Namespace == ns {
		return ref.Name
	}
	q := ref.Schema
	if q == "" {
		q = ref.Database
	}
	return q + "." + ref.Name
}
