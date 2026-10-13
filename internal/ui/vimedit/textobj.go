package vimedit

import "strings"

// textObject is the range [from, to) of i<obj> (inner) or a<obj> at p.
// obj is the key after i / a: w W " ' ` ( ) b [ ] { }.
func (b *buffer) textObject(p Pos, obj rune, inner bool) (from, to Pos, ok bool) {
	switch obj {
	case 'w', 'W':
		from, to = b.wordObject(p, obj == 'W', inner)
		return from, to, true
	case '"', '\'', '`':
		return b.quoteObject(p, obj, inner)
	case 'b':
		obj = '('
	}
	if i := strings.IndexRune(brackets, obj); i >= 0 {
		return b.bracketObject(p, rune(brackets[i&^1]), rune(brackets[i|1]), inner)
	}
	return p, p, false
}

// wordObject is iw / aw: the run of same-class runes at p; aw adds the
// blanks after it (or, if there are none, the blanks before it), and on
// blanks takes the word after them.
func (b *buffer) wordObject(p Pos, big, inner bool) (Pos, Pos) {
	line := b.lines[p.Line]
	if len(line) == 0 {
		return p, p
	}
	p.Col = min(p.Col, len(line)-1)
	run := func(col int) (int, int) {
		c := b.cls(Pos{p.Line, col}, big)
		s, e := col, col+1
		for s > 0 && b.cls(Pos{p.Line, s - 1}, big) == c {
			s--
		}
		for e < len(line) && b.cls(Pos{p.Line, e}, big) == c {
			e++
		}
		return s, e
	}
	s, e := run(p.Col)
	if inner {
		return Pos{p.Line, s}, Pos{p.Line, e}
	}
	if b.cls(p, big) == clsBlank {
		if e < len(line) {
			_, e = run(e)
		}
		return Pos{p.Line, s}, Pos{p.Line, e}
	}
	if e < len(line) && b.cls(Pos{p.Line, e}, big) == clsBlank {
		_, e = run(e)
	} else if s > 0 && b.cls(Pos{p.Line, s - 1}, big) == clsBlank {
		s, _ = run(s - 1)
	}
	return Pos{p.Line, s}, Pos{p.Line, e}
}

// quoteObject is i" / a" (and ' `) on p's line: the quoted string around
// the cursor, or else the first one after it. a" adds the quotes and the
// blanks after the closing one.
func (b *buffer) quoteObject(p Pos, q rune, inner bool) (Pos, Pos, bool) {
	line := b.lines[p.Line]
	var quotes []int
	for i, r := range line {
		if r == q {
			quotes = append(quotes, i)
		}
	}
	for i := 0; i+1 < len(quotes); i += 2 {
		first, second := quotes[i], quotes[i+1]
		if second < p.Col {
			continue
		}
		if inner {
			return Pos{p.Line, first + 1}, Pos{p.Line, second}, true
		}
		end := second + 1
		for end < len(line) && (line[end] == ' ' || line[end] == '\t') {
			end++
		}
		return Pos{p.Line, first}, Pos{p.Line, end}, true
	}
	return p, p, false
}

// bracketObject is i( / a( (and [ {): the innermost pair around p. i(
// leaves out the brackets.
func (b *buffer) bracketObject(p Pos, open, shut rune, inner bool) (Pos, Pos, bool) {
	p = b.clamp(p)
	start, ok := b.openBefore(p, open, shut)
	if !ok {
		return p, p, false
	}
	depth := 0
	for end := start; ok; end, ok = b.next(end) {
		switch b.at(end) {
		case open:
			depth++
		case shut:
			if depth--; depth > 0 {
				continue
			}
			if inner {
				return Pos{start.Line, start.Col + 1}, end, true
			}
			return start, Pos{end.Line, end.Col + 1}, true
		}
	}
	return p, p, false
}

// openBefore finds the open bracket of the innermost pair around p: p
// itself on an open bracket, the partner of a close bracket.
func (b *buffer) openBefore(p Pos, open, shut rune) (Pos, bool) {
	if b.at(p) == open {
		return p, true
	}
	q, ok := p, true
	if b.at(p) == shut {
		q, ok = b.prev(p)
	}
	depth := 0
	for ; ok; q, ok = b.prev(q) {
		switch b.at(q) {
		case shut:
			depth++
		case open:
			if depth == 0 {
				return q, true
			}
			depth--
		}
	}
	return p, false
}
