package vimedit

import (
	"strings"
	"unicode/utf8"
)

// exec runs a NORMAL-mode command.
func (e *Editor) exec(c cmd) {
	switch {
	case c.op == "" && c.motion != "":
		e.move(c)
	case isOperator(c.op):
		e.operate(c)
	default:
		e.run(c)
	}
	if e.mode != Insert {
		e.hist.commit()
		e.fit()
	}
}

// fit keeps the cursor on a rune in NORMAL and VISUAL mode (INSERT mode
// may also be at the line's end).
func (e *Editor) fit() {
	e.cur = e.buf.clamp(e.cur)
	if e.mode != Insert {
		e.cur.Col = min(e.cur.Col, max(e.buf.lineLen(e.cur.Line)-1, 0))
	}
}

// move runs a motion without an operator.
func (e *Editor) move(c cmd) {
	to, _, ok := e.motion(e.cur, c)
	if !ok {
		return
	}
	e.cur = to
	switch c.motion {
	case "j", "k", "up", "down":
	case "$":
		e.want = wantEnd
	default:
		e.want = to.Col
	}
}

// motionKind is how an operator treats a motion's range.
type motionKind int

const (
	exclusive motionKind = iota
	inclusive
	linewise
)

// motion is where c.motion goes from p.
func (e *Editor) motion(p Pos, c cmd) (Pos, motionKind, bool) {
	b := &e.buf
	n := c.n()
	switch c.motion {
	case "h", "left":
		return Pos{p.Line, max(p.Col-n, 0)}, exclusive, p.Col > 0
	case "l", "right":
		limit := b.lineLen(p.Line) - 1
		if c.op != "" {
			limit++
		}
		return Pos{p.Line, min(p.Col+n, limit)}, exclusive, p.Col < limit
	case "j", "down", "k", "up":
		l := p.Line + n
		if c.motion == "k" || c.motion == "up" {
			l = p.Line - n
		}
		l = b.clampLine(l)
		col := e.want
		if col == wantEnd || col > b.lineLen(l) {
			col = b.lineLen(l)
		}
		return Pos{l, col}, linewise, l != p.Line
	case "w", "W":
		for range n {
			p = b.wordForward(p, c.motion == "W")
		}
		return p, exclusive, true
	case "b", "B":
		for range n {
			p = b.wordBack(p, c.motion == "B")
		}
		return p, exclusive, true
	case "e", "E":
		for range n {
			p = b.wordEnd(p, c.motion == "E")
		}
		return p, inclusive, true
	case "0":
		return Pos{p.Line, 0}, exclusive, true
	case "^":
		return b.firstNonBlank(p.Line), exclusive, true
	case "$":
		l := b.clampLine(p.Line + n - 1)
		return Pos{l, max(b.lineLen(l)-1, 0)}, inclusive, true
	case "gg", "G":
		l := 0
		if c.motion == "G" {
			l = b.last()
		}
		if c.count > 0 {
			l = b.clampLine(c.count - 1)
		}
		return b.firstNonBlank(l), linewise, true
	case "%":
		to, ok := b.matchBracket(p)
		return to, inclusive, ok
	case "{", "}":
		dir := 1
		if c.motion == "{" {
			dir = -1
		}
		for range n {
			p = b.paragraph(p, dir)
		}
		return p, exclusive, true
	case "f", "t", "F", "T":
		to, ok := b.findChar(p, c.char, c.motion == "f" || c.motion == "t", c.motion == "t" || c.motion == "T", n)
		kind := inclusive
		if c.motion == "F" || c.motion == "T" {
			kind = exclusive
		}
		return to, kind, ok
	}
	return p, exclusive, false
}

// operate runs an operator over a motion, a text object or (doubled) lines.
func (e *Editor) operate(c cmd) {
	b := &e.buf
	var from, to Pos
	kind := exclusive
	switch m := c.motion; {
	case m == c.op:
		from, to, kind = e.cur, Pos{b.clampLine(e.cur.Line + c.n() - 1), 0}, linewise
	case len(m) == 2 && (m[0] == 'i' || m[0] == 'a'):
		r, _ := utf8.DecodeRuneInString(m[1:])
		var ok bool
		if from, to, ok = b.textObject(e.cur, r, m[0] == 'i'); !ok {
			return
		}
	case c.op == "c" && (m == "w" || m == "W") && b.cls(e.cur, m == "W") > clsBlank:
		// cw changes to the end of the word, like ce
		to = e.cur
		for nx, ok := b.next(to); ok && b.cls(nx, m == "W") == b.cls(e.cur, m == "W"); nx, ok = b.next(to) {
			to = nx
		}
		for range c.n() - 1 {
			to = b.wordEnd(to, m == "W")
		}
		from, kind = e.cur, inclusive
	case m == "w" || m == "W":
		// when the last word moved over ends a line, the range ends there
		from, to = e.cur, e.cur
		for i := range c.n() {
			start := to
			to = b.wordForward(to, m == "W")
			if i == c.n()-1 && to.Line > start.Line {
				to = b.lineEnd(start.Line)
			}
		}
	default:
		var ok bool
		if to, kind, ok = e.motion(e.cur, c); !ok {
			return
		}
		from = e.cur
	}
	from, to = order(from, to)
	if kind == inclusive {
		to = Pos{to.Line, min(to.Col+1, b.lineLen(to.Line))}
	}
	if kind == exclusive && to.Line > from.Line && to.Col == 0 {
		// an exclusive range ending at a line start stops at the previous line's end
		to = b.lineEnd(to.Line - 1)
		if from.Col <= b.firstNonBlank(from.Line).Col {
			kind = linewise
		}
	}
	e.apply(c, from, to, kind == linewise)
}

// apply runs operator c.op on [from, to), or on lines from.Line..to.Line.
func (e *Editor) apply(c cmd, from, to Pos, lines bool) {
	b := &e.buf
	if lines {
		a, z := from.Line, to.Line
		text := b.slice(Pos{a, 0}, b.lineEnd(z)) + "\n"
		switch c.op {
		case "d":
			e.store(c.reg, text, true)
			e.deleteLines(a, z)
		case "y":
			e.store(c.reg, text, true)
			if a < e.cur.Line {
				e.cur.Line = a
			}
		case "c":
			e.store(c.reg, text, true)
			indent := leadingBlanks(b.lines[a])
			e.cur = e.change(Pos{a, 0}, b.lineEnd(z), indent)
			e.mode = Insert
		case ">", "<":
			e.shift(a, z, c.op == ">")
		}
		return
	}
	text := b.slice(from, to)
	switch c.op {
	case "d", "c":
		e.store(c.reg, text, false)
		e.change(from, to, "")
		e.cur = from
		if c.op == "c" {
			e.mode = Insert
		}
	case "y":
		e.store(c.reg, text, false)
		e.cur = from
	case ">", "<":
		e.shift(from.Line, to.Line, c.op == ">")
	}
}

func leadingBlanks(line []rune) string {
	n := 0
	for n < len(line) && (line[n] == ' ' || line[n] == '\t') {
		n++
	}
	return string(line[:n])
}

// deleteLines deletes lines a..z and puts the cursor on the line after them.
func (e *Editor) deleteLines(a, z int) {
	b := &e.buf
	switch {
	case z < b.last():
		e.change(Pos{a, 0}, Pos{z + 1, 0}, "")
	case a > 0:
		e.change(b.lineEnd(a-1), b.lineEnd(z), "")
	default:
		e.change(Pos{}, b.end(), "")
	}
	e.cur = b.firstNonBlank(b.clampLine(a))
}

// shift indents (right) or unindents lines a..z by shiftWidth spaces; a
// leading tab counts as one level. Empty lines stay empty.
func (e *Editor) shift(a, z int, right bool) {
	for l := a; l <= z; l++ {
		line := e.buf.lines[l]
		switch {
		case len(line) == 0:
		case right:
			e.change(Pos{l, 0}, Pos{l, 0}, strings.Repeat(" ", shiftWidth))
		case line[0] == '\t':
			e.change(Pos{l, 0}, Pos{l, 1}, "")
		default:
			n := 0
			for n < shiftWidth && n < len(line) && line[n] == ' ' {
				n++
			}
			e.change(Pos{l, 0}, Pos{l, n}, "")
		}
	}
	e.cur = e.buf.firstNonBlank(a)
}

// store keeps deleted or yanked text in the unnamed register and in reg;
// "+ hands it to the host for the system clipboard.
func (e *Editor) store(reg rune, text string, linewise bool) {
	r := register{text: text, linewise: linewise}
	e.regs['"'] = r
	switch {
	case reg >= 'a' && reg <= 'z':
		e.regs[reg] = r
	case reg == '+':
		e.res.Clipboard = &text
	}
}

// change replaces from..to with s, records it for undo and returns
// where s ends.
func (e *Editor) change(from, to Pos, s string) Pos {
	from, to = order(e.buf.clamp(from), e.buf.clamp(to))
	old := e.buf.slice(from, to)
	if old == s {
		return after(from, s)
	}
	e.hist.record(edit{at: from, old: old, new: s}, e.cur)
	return e.buf.replace(from, to, s)
}

// run runs a command that is not a motion or an operator.
func (e *Editor) run(c cmd) {
	b := &e.buf
	l := e.cur.Line
	switch c.op {
	case "x":
		if b.lineLen(l) > 0 {
			end := Pos{l, min(e.cur.Col+c.n(), b.lineLen(l))}
			e.store(c.reg, b.slice(e.cur, end), false)
			e.change(e.cur, end, "")
		}
	case "D", "C":
		end := b.lineEnd(b.clampLine(l + c.n() - 1))
		e.store(c.reg, b.slice(e.cur, end), false)
		e.change(e.cur, end, "")
		if c.op == "C" {
			e.mode = Insert
		}
	case "p", "P":
		e.put(c, c.op == "P")
	case "i":
		e.mode = Insert
	case "a":
		e.mode = Insert
		e.cur.Col = min(e.cur.Col+1, b.lineLen(l))
	case "I":
		e.mode = Insert
		e.cur = b.firstNonBlank(l)
		if strings.TrimSpace(string(b.lines[l])) == "" {
			e.cur.Col = b.lineLen(l)
		}
	case "A":
		e.mode = Insert
		e.cur = b.lineEnd(l)
	case "o", "O":
		indent := leadingBlanks(b.lines[l])
		if c.op == "o" {
			e.cur = e.change(b.lineEnd(l), b.lineEnd(l), "\n"+indent)
		} else {
			e.change(Pos{l, 0}, Pos{l, 0}, indent+"\n")
			e.cur = Pos{l, utf8.RuneCountInString(indent)}
		}
		e.mode = Insert
	case "u", "ctrl+r":
		for range c.n() {
			undo := e.hist.undoStep
			if c.op == "ctrl+r" {
				undo = e.hist.redoStep
			}
			p, ok := undo(b)
			if !ok {
				break
			}
			e.cur = p
		}
	case "ctrl+d", "ctrl+u":
		dir := "j"
		if c.op == "ctrl+u" {
			dir = "k"
		}
		e.move(cmd{motion: dir, count: max(e.height/2, 1)})
	}
}

// put is p / P from c's register.
func (e *Editor) put(c cmd, before bool) {
	if c.reg == '+' {
		e.res.Paste = &ClipboardPaste{Before: before, Count: c.n(), version: e.buf.version}
		return
	}
	reg := c.reg
	if reg == 0 {
		reg = '"'
	}
	if r, ok := e.regs[reg]; ok && r.text != "" {
		e.putText(r, before, c.n())
	}
}

// putText pastes r count times: lines below (above) the cursor's line, or
// text after (before) the cursor.
func (e *Editor) putText(r register, before bool, count int) {
	b := &e.buf
	count = min(count, max(maxPut/len(r.text), 1))
	if r.linewise {
		block := strings.TrimSuffix(strings.Repeat(r.text, count), "\n")
		l := e.cur.Line
		if before {
			e.change(Pos{l, 0}, Pos{l, 0}, block+"\n")
		} else {
			e.change(b.lineEnd(l), b.lineEnd(l), "\n"+block)
			l++
		}
		e.cur = b.firstNonBlank(l)
		return
	}
	s := strings.Repeat(r.text, count)
	at := e.cur
	if !before && b.lineLen(at.Line) > 0 {
		at.Col++
	}
	end := e.change(at, at, s)
	if strings.Contains(s, "\n") {
		e.cur = at
	} else {
		e.cur = Pos{end.Line, end.Col - 1}
	}
}
