package schemadiff

import "github.com/shepherd-yun/datavim/internal/db"

// DiffIndexEdits lists the index changes of a table edited in place
// (Modify Table). Unlike DiffTable, which pairs indexes by name and then
// by definition, it pairs them by where they came from: origin[i] is the
// position in before of the index after[i] was edited from, or -1 for a
// new one. An index of before that nothing came from is dropped; one whose
// name or definition changed is dropped and added again, so renaming an
// index is a change too. Drops come in before's order, adds in after's.
// The changes point into before and after.
func DiffIndexEdits(before, after []db.Index, origin []int) (drops, adds []db.SchemaChange) {
	kept := make([]bool, len(before))
	replacement := map[int]*db.Index{} // position in before → the changed index
	for i := range after {
		o := origin[i]
		if o < 0 {
			continue
		}
		kept[o] = true
		if before[o].Name != after[i].Name || !sameIndex(before[o], after[i]) {
			replacement[o] = &after[i]
		}
	}
	for o := range before {
		if r, changed := replacement[o]; !kept[o] || changed {
			drops = append(drops, dropIndex(&before[o], r))
		}
	}
	for i := range after {
		if o := origin[i]; o < 0 || replacement[o] == &after[i] {
			adds = append(adds, addIndex(&after[i], after[i].Name))
		}
	}
	return drops, adds
}
