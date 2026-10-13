package vimedit

import "unicode/utf8"

// Selection is the visual selection as byte offsets [from, to). A
// linewise selection covers its lines without the last line break.
func (e *Editor) Selection() (from, to int, ok bool) {
	a, z, ok := e.selection()
	if !ok {
		return 0, 0, false
	}
	return e.buf.offset(a), e.buf.offset(z), true
}

func (e *Editor) selection() (Pos, Pos, bool) {
	a, z := order(e.anchor, e.cur)
	switch e.mode {
	case Visual:
		return a, Pos{z.Line, min(z.Col+1, e.buf.lineLen(z.Line))}, true
	case VisualLine:
		return Pos{a.Line, 0}, e.buf.lineEnd(z.Line), true
	}
	return Pos{}, Pos{}, false
}

func (e *Editor) leaveVisual() {
	e.mode = Normal
	e.fit()
}

// selectObject is v_iw, v_a( …: the selection becomes the text object.
func (e *Editor) selectObject(c cmd) {
	r, _ := utf8.DecodeRuneInString(c.motion[1:])
	from, to, ok := e.buf.textObject(e.cur, r, c.motion[0] == 'i')
	if !ok || !from.less(to) {
		return
	}
	e.mode, e.anchor = Visual, from
	e.cur, _ = e.buf.prev(to)
	e.want = e.cur.Col
}

// visualOp runs an operator or command on the visual selection.
func (e *Editor) visualOp(c cmd) {
	switch c.op {
	case "v", "V":
		m := Visual
		if c.op == "V" {
			m = VisualLine
		}
		if e.mode == m {
			e.leaveVisual()
		} else {
			e.mode = m
		}
		return
	case "o":
		e.anchor, e.cur = e.cur, e.anchor
		e.want = e.cur.Col
		return
	case "ctrl+d", "ctrl+u":
		e.run(c)
		e.fit()
		return
	case "x":
		c.op = "d"
	}
	from, to, _ := e.selection()
	lines := e.mode == VisualLine
	e.mode = Normal
	switch c.op {
	case "p", "P":
		reg := c.reg
		if reg == 0 {
			reg = '"'
		}
		r, ok := e.regs[reg]
		e.apply(cmd{op: "d"}, from, to, lines)
		if ok && r.text != "" {
			if !lines {
				e.cur = from
			}
			// lines deleted at the end of the text: paste below the new last line
			e.putText(r, !lines || from.Line <= e.buf.last(), 1)
		}
	default:
		e.apply(c, from, to, lines)
	}
	if e.mode != Insert {
		e.hist.commit()
		e.fit()
	}
}
