package vimedit

import "strings"

func (e *Editor) insertKey(k Key) {
	b := &e.buf
	l, col := e.cur.Line, e.cur.Col
	switch k.Name {
	case "esc":
		e.stopInsert()
		return
	case "enter":
		indent := []rune(leadingBlanks(b.lines[l]))
		e.typeText("\n" + string(indent[:min(len(indent), col)]))
	case "backspace", "ctrl+h":
		e.backspace()
	case "delete":
		if col < b.lineLen(l) || l < b.last() {
			nx, _ := b.next(e.cur)
			e.change(e.cur, nx, "")
		}
	case "ctrl+w":
		start := col
		for start > 0 && b.cls(Pos{l, start - 1}, false) == clsBlank {
			start--
		}
		if start > 0 {
			c := b.cls(Pos{l, start - 1}, false)
			for start > 0 && b.cls(Pos{l, start - 1}, false) == c {
				start--
			}
		}
		if col == 0 {
			e.backspace()
		} else {
			e.cur = e.change(Pos{l, start}, e.cur, "")
		}
	case "ctrl+u":
		if col == 0 {
			e.backspace()
		} else {
			e.cur = e.change(Pos{l, 0}, e.cur, "")
		}
	case "tab":
		e.typeText(strings.Repeat(" ", shiftWidth))
	case "left", "right", "up", "down", "home", "end":
		switch k.Name {
		case "left":
			e.cur.Col = max(col-1, 0)
		case "right":
			e.cur.Col = min(col+1, b.lineLen(l))
		case "up", "down":
			nl := l - 1
			if k.Name == "down" {
				nl = l + 1
			}
			e.cur = b.clamp(Pos{nl, col})
		case "home":
			e.cur.Col = 0
		case "end":
			e.cur.Col = b.lineLen(l)
		}
		// moving starts a new undo step; . repeats what is typed from here
		e.hist.commit()
		if e.rec != nil {
			e.rec = &change{cmd: cmd{op: "i"}}
		}
		return
	default:
		if k.Text == "" {
			e.res.Handled = false
			return
		}
		e.typeText(k.Text)
		e.record(typed{text: k.Text})
		return
	}
	e.record(typed{key: k.Name})
}

func (e *Editor) typeText(s string) { e.cur = e.change(e.cur, e.cur, s) }

func (e *Editor) backspace() {
	b := &e.buf
	if p, ok := b.prev(e.cur); ok {
		e.change(p, e.cur, "")
		e.cur = p
	}
}

func (e *Editor) stopInsert() {
	e.mode = Normal
	e.hist.commit()
	if e.rec != nil && !e.replay {
		e.dot, e.rec = e.rec, nil
	}
	if e.cur.Col > 0 {
		e.cur.Col--
	}
	e.fit()
	e.want = e.cur.Col
}
