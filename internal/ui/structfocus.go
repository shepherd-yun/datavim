package ui

import "github.com/shepherd-yun/sqlvim/internal/ui/panes/structure"

// structFocus is where the tree asked a structure tab to put its cursor:
// the row called name in section, staging its drop when drop is set.
type structFocus struct {
	section structure.Section
	name    string
	drop    bool
}

// target remembers f and applies it as soon as the tab can.
func (t *structureTab) target(f structFocus) {
	t.focus = &f
	t.applyFocus()
}

// applyFocus moves the cursor to the pending focus once the structure is
// read and, to stage a drop, once the table is being modified.
func (t *structureTab) applyFocus() {
	f := t.focus
	if f == nil || t.loading || (f.drop && t.ed == nil) {
		return
	}
	t.focus = nil
	row, ok := t.view.Find(f.section, f.name)
	if !ok {
		t.setStatus(f.name+" is not in "+t.ref.Name+" any more", true)
		return
	}
	t.view.SetCursor(f.section, row)
	if !f.drop {
		return
	}
	t.setStatus("", false)
	t.deleteRow(f.section == structure.Indexes, row)
	if t.status == "" {
		t.setStatus(joinHints("drop "+f.name+" staged", t.unsavedHint("submits", "discards")), false)
	}
}
