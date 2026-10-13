package vimedit

// typed is one edit of an INSERT session, kept so . can repeat it: a key,
// text (typed or pasted), or a completion that replaced back bytes before
// the cursor with text.
type typed struct {
	key  string
	text string
	back int
}

// change is a repeatable command and what its INSERT session typed.
type change struct {
	cmd
	typed []typed
}

// isChange reports whether . repeats commands like op.
func isChange(op string) bool {
	switch op {
	case "d", "c", ">", "<", "x", "p", "P", "D", "C", "o", "O", "i", "a", "I", "A":
		return true
	}
	return false
}

// record adds an INSERT-mode edit to the change . will repeat.
func (e *Editor) record(t typed) {
	if e.rec != nil && !e.replay {
		e.rec.typed = append(e.rec.typed, t)
	}
}

// repeat is .: the last change again, with c's count if one was typed.
func (e *Editor) repeat(c cmd) {
	if e.dot == nil {
		return
	}
	d := *e.dot
	if c.count > 0 {
		d.count = c.count
		e.dot.count = c.count
	}
	e.replay = true
	defer func() { e.replay = false }()
	e.exec(d.cmd)
	if e.mode != Insert {
		return
	}
	for _, t := range d.typed {
		switch {
		case t.key != "":
			e.insertKey(Key{Name: t.key})
		case t.back > 0:
			e.replaceBefore(t.back, t.text)
		default:
			e.typeText(t.text)
		}
	}
	e.stopInsert()
}
