package schemadiff

import (
	"slices"

	"github.com/shepherd-yun/datavim/internal/db"
)

// Writable reports whether w can write c as SQL on table t. What it cannot
// write is left to the user; foreign keys are never synced.
func Writable(w db.DDLWriter, t db.TableRef, c db.SchemaChange) bool {
	if c.Kind == db.ForeignKeyDiff {
		return false
	}
	_, manual := w.AlterTable(t, []db.SchemaChange{c})
	return len(manual) == 0
}

// Picked says whether the script includes change of object obj; change is
// -1 for a whole table to create or drop.
type Picked func(obj, change int) bool

// Script writes the picked parts of objs as statements that make the target
// like the source: table creates first (in ns, the target's namespace),
// then each table's changes, then drops. manual lists, one line each, the
// changes w cannot write, whether picked or not; foreign key differences
// are left out.
func Script(w db.DDLWriter, ns db.Namespace, objs []ObjectDiff, picked Picked) (stmts, manual []string) {
	var creates, alters, drops []string
	note := func(table string, changes []db.SchemaChange) {
		for _, c := range changes {
			manual = append(manual, table+": "+Describe(c))
		}
	}
	for oi, o := range objs {
		switch o.Kind {
		case CreateTable:
			if !picked(oi, -1) {
				continue
			}
			s := *o.Source
			s.Ref = db.TableRef{Namespace: ns, Name: o.Name}
			st, m := w.CreateTable(s)
			creates = append(creates, st...)
			note(o.Name, m)
		case DropTable:
			if picked(oi, -1) {
				drops = append(drops, w.DropTable(o.Target.Ref, false))
			}
		case AlterTable:
			var chosen, unwritable []db.SchemaChange
			for ci, c := range o.Changes {
				switch {
				case c.Kind == db.ForeignKeyDiff:
				case !Writable(w, o.Target.Ref, c):
					unwritable = append(unwritable, c)
				case picked(oi, ci):
					chosen = append(chosen, c)
				}
			}
			st, m := w.AlterTable(o.Target.Ref, chosen)
			alters = append(alters, st...)
			note(o.Name, slices.Concat(unwritable, m))
		}
	}
	return slices.Concat(creates, alters, drops), manual
}
