package vimedit

// maxUndo bounds the undo history.
const maxUndo = 1000

// edit is one replacement: at at, old was replaced by new.
type edit struct {
	at       Pos
	old, new string
}

// step is what one u undoes: the edits of one command or INSERT session.
type step struct {
	edits  []edit
	cursor Pos // the cursor before the step
}

// history records edits in steps for undo and redo.
type history struct {
	undo, redo []step
	open       *step
}

// record adds e to the open step, opening one (with the cursor before it)
// if needed. A new edit drops what could be redone.
func (h *history) record(e edit, cursor Pos) {
	h.redo = nil
	if h.open == nil {
		h.open = &step{cursor: cursor}
	}
	if n := len(h.open.edits); n > 0 {
		// typing: grow the previous insertion instead of adding an edit per key
		if last := &h.open.edits[n-1]; last.old == "" && e.old == "" && after(last.at, last.new) == e.at {
			last.new += e.new
			return
		}
	}
	h.open.edits = append(h.open.edits, e)
}

// commit closes the open step.
func (h *history) commit() {
	if h.open == nil {
		return
	}
	if len(h.open.edits) > 0 {
		h.undo = append(h.undo, *h.open)
		if over := len(h.undo) - maxUndo; over > 0 {
			h.undo = h.undo[over:]
		}
	}
	h.open = nil
}

func (h *history) reset() { *h = history{} }

// undoStep reverts the last step on b and returns where the cursor goes.
func (h *history) undoStep(b *buffer) (Pos, bool) {
	h.commit()
	if len(h.undo) == 0 {
		return Pos{}, false
	}
	s := h.undo[len(h.undo)-1]
	h.undo = h.undo[:len(h.undo)-1]
	for i := len(s.edits) - 1; i >= 0; i-- {
		e := s.edits[i]
		b.replace(e.at, after(e.at, e.new), e.old)
	}
	h.redo = append(h.redo, s)
	return s.cursor, true
}

// redoStep applies the last undone step again and returns where the
// cursor goes: where its first edit starts.
func (h *history) redoStep(b *buffer) (Pos, bool) {
	if len(h.redo) == 0 {
		return Pos{}, false
	}
	s := h.redo[len(h.redo)-1]
	h.redo = h.redo[:len(h.redo)-1]
	for _, e := range s.edits {
		b.replace(e.at, after(e.at, e.old), e.new)
	}
	h.undo = append(h.undo, s)
	return s.edits[0].at, true
}
