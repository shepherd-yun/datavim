package editor

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/ui/highlight"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

const (
	tabWidth    = 4
	placeholder = "i to write SQL · enter runs the statement under the cursor"
)

// cell is one rune as drawn.
type cell struct {
	text  string
	width int
	class highlight.Class
}

func cellOf(r rune, class highlight.Class) cell {
	if r == '\t' {
		return cell{text: strings.Repeat(" ", tabWidth), width: tabWidth, class: class}
	}
	s := string(r)
	w := ansi.StringWidth(s)
	if unicode.IsControl(r) || w == 0 {
		return cell{text: "�", width: 1, class: class}
	}
	return cell{text: s, width: w, class: class}
}

// row is one screen row: cells[from:to] of a logical line.
type row struct {
	line     int
	first    bool // the line's first row; it shows the line number
	cells    []cell
	from, to int
	cursor   int // index into cells drawn as the cursor (to = after the text), or -1
}

// classes returns the highlight class of every byte of text, memoized.
func (m *Model) classes(text string) []highlight.Class {
	if m.hlClasses != nil && text == m.hlText {
		return m.hlClasses
	}
	cls := make([]highlight.Class, len(text))
	if m.highlight != nil {
		for _, s := range m.highlight(text) {
			for i := max(s.Start, 0); i < s.End && i < len(cls); i++ {
				cls[i] = s.Class
			}
		}
	}
	m.hlText, m.hlClasses = text, cls
	return cls
}

// wrap splits cells into runs no wider than width; a wide rune that does
// not fit starts the next run. There is always at least one run.
func wrap(cells []cell, width int) [][2]int {
	var runs [][2]int
	from, w := 0, 0
	for i, c := range cells {
		if w+c.width > width && i > from {
			runs = append(runs, [2]int{from, i})
			from, w = i, 0
		}
		w += c.width
	}
	return append(runs, [2]int{from, len(cells)})
}

func widthOf(cells []cell) int {
	w := 0
	for _, c := range cells {
		w += c.width
	}
	return w
}

// View draws the visible rows: line numbers, colored text, block cursor.
func (m *Model) View() string {
	if m.height <= 0 || m.width <= 0 {
		return ""
	}
	text := m.ta.Value()
	lines := strings.Split(text, "\n")
	digits := max(len(strconv.Itoa(len(lines))), 2)
	gutter := digits + 1
	textW := max(m.width-gutter, 1)
	cls := m.classes(text)
	curLine, curCol := m.ta.Line(), m.ta.Column()

	var rows []row
	curRow, curX := 0, 0
	off := 0
	for i, l := range lines {
		cells := make([]cell, 0, len(l))
		for j, r := range l {
			cells = append(cells, cellOf(r, cls[off+j]))
		}
		off += len(l) + 1
		runs := wrap(cells, textW)
		for k, run := range runs {
			rw := row{line: i, first: k == 0, cells: cells, from: run[0], to: run[1], cursor: -1}
			last := k == len(runs)-1
			if c := min(curCol, len(cells)); i == curLine && c >= run[0] && (c < run[1] || last && c == run[1]) {
				x := widthOf(cells[run[0]:c])
				if x >= textW { // after a full last row: the cursor gets a row of its own
					rows = append(rows, rw)
					rw = row{line: i, cells: cells, from: c, to: c}
					x = 0
				}
				rw.cursor, curRow, curX = c, len(rows), x
			}
			rows = append(rows, rw)
		}
	}

	if curRow < m.top {
		m.top = curRow
	}
	if curRow >= m.top+m.height {
		m.top = curRow - m.height + 1
	}
	m.top = textutil.Clamp(m.top, 0, max(len(rows)-1, 0))
	m.curX, m.curY = gutter+curX, curRow-m.top

	out := make([]string, 0, m.height)
	for r := m.top; r < len(rows) && len(out) < m.height; r++ {
		out = append(out, drawRow(rows[r], digits, textW, curLine, text == ""))
	}
	for len(out) < m.height {
		out = append(out, strings.Repeat(" ", m.width))
	}
	return strings.Join(out, "\n")
}

func drawRow(r row, digits, textW, curLine int, empty bool) string {
	gutter := strings.Repeat(" ", digits+1)
	if r.first {
		num := fmt.Sprintf("%*d ", digits, r.line+1)
		if r.line == curLine {
			gutter = num
		} else {
			gutter = theme.Muted.Render(num)
		}
	}
	var b, run strings.Builder
	class := highlight.Plain
	flush := func() {
		if run.Len() == 0 {
			return
		}
		if class == highlight.Plain {
			b.WriteString(run.String())
		} else {
			b.WriteString(theme.Syntax(class).Render(run.String()))
		}
		run.Reset()
	}
	for i := r.from; i < r.to; i++ {
		c := r.cells[i]
		if i == r.cursor {
			flush()
			b.WriteString(theme.Selected.Render(c.text))
			continue
		}
		if c.class != class {
			flush()
			class = c.class
		}
		run.WriteString(c.text)
	}
	flush()
	if r.cursor == r.to {
		b.WriteString(theme.Selected.Render(" "))
		if empty {
			b.WriteString(theme.Muted.Render(placeholder))
		}
	}
	return gutter + textutil.Fit(b.String(), textW)
}
