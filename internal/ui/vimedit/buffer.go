// Package vimedit is the console's vim editing engine: a text buffer with
// a cursor, modes, motions, operators, text objects, registers, undo and
// dot-repeat. It knows nothing about terminals: keys come in as Key values
// and the host (internal/ui/panes/editor) draws the text.
package vimedit

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// Pos is a place in the text: a line and a rune column, both 0-based.
// Col may equal the line's length: the position of its line break.
type Pos struct{ Line, Col int }

func (p Pos) less(q Pos) bool { return p.Line < q.Line || p.Line == q.Line && p.Col < q.Col }

func order(a, b Pos) (Pos, Pos) {
	if b.less(a) {
		return b, a
	}
	return a, b
}

// buffer is the text as lines of runes; there is always at least one line.
type buffer struct {
	lines   [][]rune
	text    string // Text() cache, valid while !dirty
	dirty   bool
	version int // bumped by every change
}

func newBuffer(s string) buffer { return buffer{lines: splitLines(s), text: s} }

func splitLines(s string) [][]rune {
	parts := strings.Split(s, "\n")
	out := make([][]rune, len(parts))
	for i, p := range parts {
		out[i] = []rune(p)
	}
	return out
}

func (b *buffer) Text() string {
	if b.dirty {
		parts := make([]string, len(b.lines))
		for i, l := range b.lines {
			parts[i] = string(l)
		}
		b.text, b.dirty = strings.Join(parts, "\n"), false
	}
	return b.text
}

func (b *buffer) last() int           { return len(b.lines) - 1 }
func (b *buffer) lineLen(l int) int   { return len(b.lines[l]) }
func (b *buffer) empty(l int) bool    { return len(b.lines[l]) == 0 }
func (b *buffer) end() Pos            { return Pos{b.last(), b.lineLen(b.last())} }
func (b *buffer) lineEnd(l int) Pos   { return Pos{l, b.lineLen(l)} }
func (b *buffer) clampLine(l int) int { return min(max(l, 0), b.last()) }

// clamp keeps p inside the text; Col may reach the line's length.
func (b *buffer) clamp(p Pos) Pos {
	p.Line = b.clampLine(p.Line)
	p.Col = min(max(p.Col, 0), b.lineLen(p.Line))
	return p
}

func runeBytes(rs []rune) int {
	n := 0
	for _, r := range rs {
		n += utf8.RuneLen(r)
	}
	return n
}

// offset is p as a byte offset into Text().
func (b *buffer) offset(p Pos) int {
	p = b.clamp(p)
	off := 0
	for _, l := range b.lines[:p.Line] {
		off += runeBytes(l) + 1
	}
	return off + runeBytes(b.lines[p.Line][:p.Col])
}

// pos is the position of byte offset off (clamped; a byte inside a rune
// counts as that rune).
func (b *buffer) pos(off int) Pos {
	off = max(off, 0)
	for i, l := range b.lines {
		n := runeBytes(l)
		if off <= n || i == b.last() {
			col := 0
			for _, r := range l {
				if off <= 0 {
					break
				}
				off -= utf8.RuneLen(r)
				if off >= 0 {
					col++
				}
			}
			return Pos{i, col}
		}
		off -= n + 1
	}
	return b.end()
}

// slice is the text from from to to (exclusive).
func (b *buffer) slice(from, to Pos) string {
	from, to = order(b.clamp(from), b.clamp(to))
	if from.Line == to.Line {
		return string(b.lines[from.Line][from.Col:to.Col])
	}
	var s strings.Builder
	s.WriteString(string(b.lines[from.Line][from.Col:]))
	for l := from.Line + 1; l < to.Line; l++ {
		s.WriteByte('\n')
		s.WriteString(string(b.lines[l]))
	}
	s.WriteByte('\n')
	s.WriteString(string(b.lines[to.Line][:to.Col]))
	return s.String()
}

// replace puts s in place of the text from from to to and returns the
// position right after s.
func (b *buffer) replace(from, to Pos, s string) Pos {
	from, to = order(b.clamp(from), b.clamp(to))
	head := b.lines[from.Line][:from.Col]
	tail := b.lines[to.Line][to.Col:]
	ins := splitLines(s)
	ins[0] = append(slices.Clone(head), ins[0]...)
	last := len(ins) - 1
	end := Pos{from.Line + last, len(ins[last])}
	ins[last] = append(ins[last], tail...)
	b.lines = slices.Replace(b.lines, from.Line, to.Line+1, ins...)
	b.dirty = true
	b.version++
	return end
}

// after is where s ends when it is inserted at p.
func after(p Pos, s string) Pos {
	n := strings.Count(s, "\n")
	if n == 0 {
		return Pos{p.Line, p.Col + utf8.RuneCountInString(s)}
	}
	return Pos{p.Line + n, utf8.RuneCountInString(s[strings.LastIndexByte(s, '\n')+1:])}
}
