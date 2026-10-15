// Package schemadiff computes the changes that make one table, or one
// namespace, look like another. It writes no SQL: drivers do that
// (db.DDLWriter), so this package knows nothing about dialects.
package schemadiff

import (
	"slices"
	"sort"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// DiffTable lists the changes that make target look like source, in the
// order they can be applied: index drops, column renames, column drops,
// column adds, column alters, index adds, the table's comment and options,
// then foreign key differences (shown, never synced). Columns pair by name;
// renames (old name → new name) pairs a target column with a renamed
// source column, which only Modify Table passes. The changes point into
// target and source.
func DiffTable(target, source db.TableSchema, renames map[string]string) []db.SchemaChange {
	cols := diffColumns(target, source, renames)
	indexDrops, indexAdds := diffIndexes(target, source)
	out := slices.Concat(indexDrops, cols, indexAdds, diffTable(target, source))
	return append(out, diffForeignKeys(target, source)...)
}

// diffColumns returns renames, drops, adds and alters, in that order.
func diffColumns(target, source db.TableSchema, renames map[string]string) []db.SchemaChange {
	srcByName := map[string]*db.Column{}
	for i := range source.Columns {
		srcByName[source.Columns[i].Name] = &source.Columns[i]
	}
	matched := map[string]bool{}
	var renamed, drops, adds, alters []db.SchemaChange
	for i := range target.Columns {
		old := &target.Columns[i]
		name := old.Name
		if to, ok := renames[name]; ok {
			name = to
		}
		nw, ok := srcByName[name]
		if !ok {
			drops = append(drops, db.SchemaChange{Kind: db.DropColumn, Old: old, Risk: db.Destructive})
			continue
		}
		matched[name] = true
		if name != old.Name {
			renamed = append(renamed, db.SchemaChange{Kind: db.RenameColumn, Old: old, New: nw})
		}
		if attrs := columnAttrs(*old, *nw); attrs != 0 {
			alters = append(alters, db.SchemaChange{Kind: db.AlterColumn, Old: old, New: nw, Attrs: attrs, Risk: alterRisk(*old, *nw, attrs)})
		}
	}
	for i := range source.Columns {
		c := &source.Columns[i]
		if matched[c.Name] {
			continue
		}
		ch := db.SchemaChange{Kind: db.AddColumn, New: c}
		if i > 0 {
			ch.After = source.Columns[i-1].Name
		}
		if !c.Nullable && c.Default == nil && !c.AutoIncrement && c.Generated == "" {
			ch.Risk = db.Lossy // fails on a table with rows
		}
		adds = append(adds, ch)
	}
	return slices.Concat(renamed, drops, adds, alters)
}

func columnAttrs(old, nw db.Column) db.ColumnAttr {
	var a db.ColumnAttr
	if old.Type != nw.Type {
		a |= db.AttrType
	}
	if old.Nullable != nw.Nullable {
		a |= db.AttrNullable
	}
	if (old.Default == nil) != (nw.Default == nil) || old.Default != nil && *old.Default != *nw.Default {
		a |= db.AttrDefault
	}
	if old.Comment != nw.Comment {
		a |= db.AttrComment
	}
	if old.Collation != nw.Collation {
		a |= db.AttrCollation
	}
	if old.AutoIncrement != nw.AutoIncrement {
		a |= db.AttrAutoIncrement
	}
	if old.Generated != nw.Generated {
		a |= db.AttrGenerated
	}
	return a
}

func alterRisk(old, nw db.Column, a db.ColumnAttr) db.Risk {
	if a&db.AttrType != 0 && LossyType(old.Type, nw.Type) || a&db.AttrNullable != 0 && !nw.Nullable {
		return db.Lossy
	}
	return 0
}

func sameIndex(a, b db.Index) bool {
	return slices.Equal(a.Columns, b.Columns) && a.Unique == b.Unique && a.Primary == b.Primary &&
		a.Method == b.Method && a.Where == b.Where
}

// diffIndexes pairs the primary keys, then indexes by name, then the rest
// by definition, so that an index named differently on the other side is
// no change. A changed index is dropped and added again under the
// target's name.
func diffIndexes(target, source db.TableSchema) (drops, adds []db.SchemaChange) {
	tgt := make([]*db.Index, len(target.Indexes))
	for i := range target.Indexes {
		tgt[i] = &target.Indexes[i]
	}
	src := make([]*db.Index, len(source.Indexes))
	for i := range source.Indexes {
		src[i] = &source.Indexes[i]
	}
	var pairs [][2]*db.Index
	take := func(match func(t, s *db.Index) bool) {
		for i := range tgt {
			for j := range src {
				if tgt[i] != nil && src[j] != nil && match(tgt[i], src[j]) {
					pairs = append(pairs, [2]*db.Index{tgt[i], src[j]})
					tgt[i], src[j] = nil, nil
					break
				}
			}
		}
	}
	take(func(t, s *db.Index) bool { return t.Primary && s.Primary })
	take(func(t, s *db.Index) bool { return t.Name == s.Name })
	take(func(t, s *db.Index) bool { return sameIndex(*t, *s) })

	drop := func(old, replacement *db.Index) {
		ch := db.SchemaChange{Kind: db.DropIndex, OldIndex: old}
		if old.Unique && (replacement == nil || !replacement.Unique) {
			ch.Risk = db.Destructive
		}
		drops = append(drops, ch)
	}
	add := func(ix *db.Index, name string) {
		cp := *ix
		cp.Name = name
		ch := db.SchemaChange{Kind: db.AddIndex, NewIndex: &cp}
		if cp.Unique {
			ch.Risk = db.Lossy
		}
		adds = append(adds, ch)
	}
	for _, p := range pairs {
		if !sameIndex(*p[0], *p[1]) {
			drop(p[0], p[1])
			add(p[1], p[0].Name)
		}
	}
	for _, t := range tgt {
		if t != nil {
			drop(t, nil)
		}
	}
	for _, s := range src {
		if s != nil {
			add(s, indexName(s.Name, source.Ref.Name, target.Ref.Name))
		}
	}
	return drops, adds
}

// indexName is the name source's index gets on target's table. Names that
// start with the source table's name follow the target's, so comparing
// users with users_bak in one PostgreSQL schema, where index names are
// unique, does not try to reuse users's names.
func indexName(name, srcTable, tgtTable string) string {
	if srcTable != tgtTable {
		if rest, ok := strings.CutPrefix(name, srcTable+"_"); ok {
			return tgtTable + "_" + rest
		}
	}
	return name
}

// diffTable compares the table's own comment and options. Options the
// source does not have (PostgreSQL has none) are left alone.
func diffTable(target, source db.TableSchema) []db.SchemaChange {
	var out []db.SchemaChange
	if target.Comment != source.Comment {
		out = append(out, db.SchemaChange{Kind: db.AlterTableComment, OldValue: target.Comment, NewValue: source.Comment})
	}
	keys := make([]string, 0, len(source.Options))
	for k := range source.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := source.Options[k]; v != target.Options[k] {
			out = append(out, db.SchemaChange{Kind: db.AlterTableOption, Option: k, OldValue: target.Options[k], NewValue: v})
		}
	}
	return out
}

// sameForeignKey compares two foreign keys of tables in possibly different
// namespaces: a reference into the table's own namespace matches one into
// the other table's own namespace.
func sameForeignKey(a db.ForeignKey, aNS db.Namespace, b db.ForeignKey, bNS db.Namespace) bool {
	aLocal, bLocal := a.RefTable.Namespace == aNS, b.RefTable.Namespace == bNS
	sameRef := aLocal && bLocal && a.RefTable.Name == b.RefTable.Name || !aLocal && !bLocal && a.RefTable == b.RefTable
	return sameRef && slices.Equal(a.Columns, b.Columns) && slices.Equal(a.RefColumns, b.RefColumns)
}

// diffForeignKeys pairs foreign keys by name, then by definition, and
// reports the differences: the source's extra keys first, then changed
// ones, then the target's extra keys.
func diffForeignKeys(target, source db.TableSchema) []db.SchemaChange {
	tNS, sNS := target.Ref.Namespace, source.Ref.Namespace
	tgt := make([]*db.ForeignKey, len(target.ForeignKeys))
	for i := range target.ForeignKeys {
		tgt[i] = &target.ForeignKeys[i]
	}
	var added, changed []db.SchemaChange
	for i := range source.ForeignKeys {
		s := &source.ForeignKeys[i]
		match := -1
		for j, t := range tgt {
			if t != nil && t.Name == s.Name {
				match = j
				break
			}
		}
		if match < 0 {
			for j, t := range tgt {
				if t != nil && sameForeignKey(*t, tNS, *s, sNS) {
					match = j
					break
				}
			}
		}
		if match < 0 {
			added = append(added, db.SchemaChange{Kind: db.ForeignKeyDiff, NewFK: s})
			continue
		}
		if t := tgt[match]; !sameForeignKey(*t, tNS, *s, sNS) {
			changed = append(changed, db.SchemaChange{Kind: db.ForeignKeyDiff, OldFK: t, NewFK: s})
		}
		tgt[match] = nil
	}
	var removed []db.SchemaChange
	for _, t := range tgt {
		if t != nil {
			removed = append(removed, db.SchemaChange{Kind: db.ForeignKeyDiff, OldFK: t})
		}
	}
	return slices.Concat(added, changed, removed)
}
