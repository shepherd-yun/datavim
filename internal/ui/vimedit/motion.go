package vimedit

import (
	"strings"
	"unicode"
)

// Character classes for word motions. A line break is its own class.
const (
	clsBreak = -1
	clsBlank = 0
	clsWord  = 1
	clsPunct = 2
)

// at is the rune at p, '\n' at a line's end.
func (b *buffer) at(p Pos) rune {
	if p.Col >= b.lineLen(p.Line) {
		return '\n'
	}
	return b.lines[p.Line][p.Col]
}

// cls is the class of the rune at p; big (W, B, E) makes every
// non-blank rune part of a word.
func (b *buffer) cls(p Pos, big bool) int {
	r := b.at(p)
	switch {
	case r == '\n':
		return clsBreak
	case r == ' ' || r == '\t':
		return clsBlank
	case big || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return clsWord
	}
	return clsPunct
}

// next is the position after p, walking line breaks; false at the end.
func (b *buffer) next(p Pos) (Pos, bool) {
	if p.Col < b.lineLen(p.Line) {
		return Pos{p.Line, p.Col + 1}, true
	}
	if p.Line < b.last() {
		return Pos{p.Line + 1, 0}, true
	}
	return p, false
}

// prev is the position before p; false at the start.
func (b *buffer) prev(p Pos) (Pos, bool) {
	if p.Col > 0 {
		return Pos{p.Line, min(p.Col, b.lineLen(p.Line)) - 1}, true
	}
	if p.Line > 0 {
		return b.lineEnd(p.Line - 1), true
	}
	return p, false
}

// wordForward is w / W: the start of the next word. An empty line counts
// as a word. At the last word it stops at the end of the text.
func (b *buffer) wordForward(p Pos, big bool) Pos {
	ok := true
	if c := b.cls(p, big); c > clsBlank {
		for ok && b.cls(p, big) == c {
			p, ok = b.next(p)
		}
	}
	for ok {
		switch b.cls(p, big) {
		case clsBreak:
			if p, ok = b.next(p); ok && b.empty(p.Line) {
				return p
			}
		case clsBlank:
			p, ok = b.next(p)
		default:
			return p
		}
	}
	return p
}

// wordEnd is e / E: the last rune of this or the next word.
func (b *buffer) wordEnd(p Pos, big bool) Pos {
	start := p
	p, ok := b.next(p)
	for ok && b.cls(p, big) <= clsBlank {
		p, ok = b.next(p)
	}
	if !ok {
		if b.cls(p, big) <= clsBlank {
			return start
		}
		return p
	}
	c := b.cls(p, big)
	for {
		n, ok := b.next(p)
		if !ok || b.cls(n, big) != c {
			return p
		}
		p = n
	}
}

// wordBack is b / B: the start of this or the previous word. An empty
// line counts as a word.
func (b *buffer) wordBack(p Pos, big bool) Pos {
	p, ok := b.prev(p)
	for ok && b.cls(p, big) <= clsBlank {
		if b.empty(p.Line) {
			return p
		}
		p, ok = b.prev(p)
	}
	c := b.cls(p, big)
	for {
		q, ok := b.prev(p)
		if !ok || b.cls(q, big) != c {
			return p
		}
		p = q
	}
}

// firstNonBlank is ^: the first rune of line l that is not a blank.
func (b *buffer) firstNonBlank(l int) Pos {
	for i, r := range b.lines[l] {
		if r != ' ' && r != '\t' {
			return Pos{l, i}
		}
	}
	return Pos{l, max(b.lineLen(l)-1, 0)}
}

// findChar is f / F / t / T within the line: the count-th r after
// (forward) or before p; till stops one rune short.
func (b *buffer) findChar(p Pos, r rune, forward, till bool, count int) (Pos, bool) {
	line := b.lines[p.Line]
	col := p.Col
	for n := 0; n < count; {
		if forward {
			col++
		} else {
			col--
		}
		if col < 0 || col >= len(line) {
			return p, false
		}
		if line[col] == r {
			n++
		}
	}
	if till {
		if forward {
			col--
		} else {
			col++
		}
	}
	return Pos{p.Line, col}, true
}

const brackets = "()[]{}"

// matchBracket is %: from the first bracket at or after p on its line to
// the bracket that pairs with it.
func (b *buffer) matchBracket(p Pos) (Pos, bool) {
	line := b.lines[p.Line]
	col := p.Col
	for col < len(line) && !strings.ContainsRune(brackets, line[col]) {
		col++
	}
	if col >= len(line) {
		return p, false
	}
	open := line[col]
	i := strings.IndexRune(brackets, open)
	pair := rune(brackets[i^1])
	forward := i%2 == 0
	depth := 0
	q, ok := Pos{p.Line, col}, true
	for ok {
		switch b.at(q) {
		case open:
			depth++
		case pair:
			depth--
			if depth == 0 {
				return q, true
			}
		}
		if forward {
			q, ok = b.next(q)
		} else {
			q, ok = b.prev(q)
		}
	}
	return p, false
}

// paragraph is } (dir 1) and { (dir -1): the next empty line after the
// current paragraph, or the start / end of the text.
func (b *buffer) paragraph(p Pos, dir int) Pos {
	l := p.Line
	for l >= 0 && l <= b.last() && b.empty(l) { // leave the empty lines the cursor is on
		l += dir
	}
	for l >= 0 && l <= b.last() && !b.empty(l) { // cross the paragraph
		l += dir
	}
	switch {
	case l < 0:
		return Pos{}
	case l > b.last():
		return b.end()
	}
	return Pos{l, 0}
}
