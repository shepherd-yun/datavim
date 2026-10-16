// Package textview is a scrolling, read-only view of lines of text.
package textview

import (
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
)

// Model holds the lines and the first visible one.
type Model struct {
	lines         []string
	offset        int
	width, height int
}

// New builds an empty view.
func New() *Model { return &Model{} }

// SetLines replaces the text; the scroll position is kept when it still fits.
func (m *Model) SetLines(lines []string) {
	m.lines = lines
	m.clamp()
}

// SetSize sets the visible area.
func (m *Model) SetSize(w, h int) {
	m.width, m.height = max(w, 0), max(h, 0)
	m.clamp()
}

// Offset is the index of the first visible line.
func (m *Model) Offset() int { return m.offset }

func (m *Model) clamp() {
	m.offset = textutil.Clamp(m.offset, 0, max(len(m.lines)-max(m.height, 1), 0))
}

// Move scrolls by d lines.
func (m *Model) Move(d int) {
	m.offset += d
	m.clamp()
}

func (m *Model) Top()    { m.offset = 0 }
func (m *Model) Bottom() { m.Move(len(m.lines)) }

// HalfPage scrolls half the height in direction dir (+1 / -1).
func (m *Model) HalfPage(dir int) { m.Move(dir * max(m.height/2, 1)) }

// Find scrolls to the next line containing q (ignoring case) after (or
// before) the first visible one, wrapping around.
func (m *Model) Find(q string, forward bool) bool {
	n := len(m.lines)
	if q == "" || n == 0 {
		return false
	}
	q = strings.ToLower(q)
	step := 1
	if !forward {
		step = -1
	}
	for i := 1; i <= n; i++ {
		j := ((m.offset+step*i)%n + n) % n
		if strings.Contains(strings.ToLower(m.lines[j]), q) {
			m.offset = j
			m.clamp()
			return true
		}
	}
	return false
}

// View renders the visible lines, each fitted to the width.
func (m *Model) View(bool) string {
	end := min(m.offset+m.height, len(m.lines))
	out := make([]string, 0, m.height)
	for _, l := range m.lines[min(m.offset, end):end] {
		out = append(out, textutil.Fit(textutil.Sanitize(l), m.width))
	}
	return strings.Join(out, "\n")
}
