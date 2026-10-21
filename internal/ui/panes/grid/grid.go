// Package grid renders a result set as a navigable table.
package grid

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

const (
	sep      = " │ "
	sepWidth = 3
	// widthSample bounds the bytes measured per cell so huge values stay cheap.
	widthSample = 1024
)

// Mark is how a cell is drawn besides its value: staged data edits.
type Mark int

const (
	MarkNone     Mark = iota
	MarkChanged       // a staged new value
	MarkInserted      // a cell of a new row given a value
	MarkDefault       // a cell of a new row left to the column default
	MarkDeleted       // a cell of a row marked for deletion
	MarkError         // a cell of a row in trouble (an offline partition)
	MarkSet           // a value set away from its default
)

// Model is a table with a cell cursor.
type Model struct {
	rs             *db.ResultSet
	maxColumnWidth int
	widths         []int
	row, col       int
	rowOff, colOff int
	width, height  int
	marks          func(row, col int) Mark
	words          func(row, col int) []string
	sortCol        string
	sortDesc       bool
}

// New builds a grid; rs may be nil.
func New(rs *db.ResultSet, maxColumnWidth int) *Model {
	if maxColumnWidth <= 0 {
		maxColumnWidth = 40
	}
	m := &Model{maxColumnWidth: maxColumnWidth}
	m.SetResult(rs)
	return m
}

// SetResult replaces the data and resets the cursor.
func (m *Model) SetResult(rs *db.ResultSet) {
	m.rs = rs
	m.row, m.col, m.rowOff, m.colOff = 0, 0, 0, 0
	m.measure()
}

// measure sizes the columns to their headers and cells.
func (m *Model) measure() {
	m.widths = nil
	if m.rs == nil {
		return
	}
	m.widths = make([]int, len(m.rs.Columns))
	for c := range m.rs.Columns {
		w := ansi.StringWidth(m.headerText(c))
		for r, row := range m.rs.Rows {
			_, text := m.look(r, c, row[c])
			w = max(w, ansi.StringWidth(text))
		}
		m.widths[c] = textutil.Clamp(w, 1, m.maxColumnWidth)
	}
}

// SetMarks sets how cells are marked (nil: no marks).
func (m *Model) SetMarks(f func(row, col int) Mark) {
	m.marks = f
	m.measure()
}

// SetHighlight marks the words f gives for a cell (search hits) in the
// highlight color; nil marks none.
func (m *Model) SetHighlight(f func(row, col int) []string) { m.words = f }

// SetSort shows which column the rows are sorted by ("" for none).
func (m *Model) SetSort(col string, desc bool) {
	m.sortCol, m.sortDesc = col, desc
	m.measure()
}

func (m *Model) headerText(c int) string {
	name := m.rs.Columns[c].Name
	text := textutil.Sanitize(name)
	switch {
	case name != m.sortCol || m.sortCol == "":
	case m.sortDesc:
		text += " ▼"
	default:
		text += " ▲"
	}
	return text
}

// look is a cell's style and text, its mark included.
func (m *Model) look(r, c int, v db.Value) (lipgloss.Style, string) {
	style, text := lipgloss.NewStyle(), cellText(v)
	if v.Null {
		style = theme.Null
	}
	mark := MarkNone
	if m.marks != nil {
		mark = m.marks(r, c)
	}
	switch mark {
	case MarkChanged:
		style = theme.Changed
	case MarkInserted:
		style = theme.Inserted
	case MarkDefault:
		style, text = theme.Null, "DEFAULT"
	case MarkDeleted:
		style = theme.Deleted
	case MarkError:
		style = theme.Error
	case MarkSet:
		style = theme.Accent
	}
	return style, text
}

// Position is the cursor's row and column.
func (m *Model) Position() (row, col int) { return m.row, m.col }

// SetCursor moves the cursor, clamped to the data.
func (m *Model) SetCursor(row, col int) {
	m.row, m.col = row, col
	m.Move(0, 0)
}

// Column is the column under the cursor, also when there are no rows.
func (m *Model) Column() (db.ColumnInfo, bool) {
	if m.rs == nil || m.colCount() == 0 {
		return db.ColumnInfo{}, false
	}
	return m.rs.Columns[m.col], true
}

// SetSize sets the drawing area.
func (m *Model) SetSize(w, h int) { m.width, m.height = max(w, 0), max(h, 0) }

// RowCount returns the number of data rows.
func (m *Model) RowCount() int {
	if m.rs == nil {
		return 0
	}
	return len(m.rs.Rows)
}

func (m *Model) colCount() int { return len(m.widths) }

// Move moves the cursor, clamped to the data.
func (m *Model) Move(dRow, dCol int) {
	m.row = textutil.Clamp(m.row+dRow, 0, m.RowCount()-1)
	m.col = textutil.Clamp(m.col+dCol, 0, m.colCount()-1)
}

func (m *Model) Top()       { m.row = 0 }
func (m *Model) Bottom()    { m.row = max(m.RowCount()-1, 0) }
func (m *Model) LineStart() { m.col = 0 }
func (m *Model) LineEnd()   { m.col = max(m.colCount()-1, 0) }

// HalfPage moves half the visible rows in direction dir.
func (m *Model) HalfPage(dir int) { m.Move(dir*max(m.bodyHeight()/2, 1), 0) }

// Cell returns the cell under the cursor.
func (m *Model) Cell() (db.ColumnInfo, db.Value, bool) {
	if m.RowCount() == 0 || m.colCount() == 0 {
		return db.ColumnInfo{}, db.Value{}, false
	}
	return m.rs.Columns[m.col], m.rs.Rows[m.row][m.col], true
}

func (m *Model) bodyHeight() int { return max(m.height-2, 0) }

func (m *Model) spanWidth(from, to int) int {
	w := 0
	for c := from; c <= to; c++ {
		w += m.widths[c]
		if c > from {
			w += sepWidth
		}
	}
	return w
}

func (m *Model) scrollIntoView() {
	if body := m.bodyHeight(); body > 0 {
		if m.row < m.rowOff {
			m.rowOff = m.row
		}
		if m.row >= m.rowOff+body {
			m.rowOff = m.row - body + 1
		}
	}
	if m.col < m.colOff {
		m.colOff = m.col
	}
	for m.colOff < m.col && m.spanWidth(m.colOff, m.col) > m.width {
		m.colOff++
	}
}

func (m *Model) visibleColumns() []int {
	var cols []int
	used := 0
	for c := m.colOff; c < m.colCount(); c++ {
		w := m.widths[c]
		if len(cols) > 0 {
			w += sepWidth
			if used+w > m.width {
				break
			}
		}
		cols = append(cols, c)
		used += w
	}
	return cols
}

// View renders exactly the visible part of the grid, each line m.width wide.
func (m *Model) View(focused bool) string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.rs == nil || m.colCount() == 0 {
		return textutil.Fit(theme.Muted.Render("(no columns)"), m.width)
	}
	m.scrollIntoView()
	cols := m.visibleColumns()
	sepStyled := theme.Muted.Render(sep)

	header := make([]string, len(cols))
	rule := make([]string, len(cols))
	for i, c := range cols {
		header[i] = theme.Header.Render(textutil.Fit(m.headerText(c), m.widths[c]))
		rule[i] = strings.Repeat("─", m.widths[c])
	}
	lines := []string{
		strings.Join(header, sepStyled),
		theme.Muted.Render(strings.Join(rule, "─┼─")),
	}
	if m.RowCount() == 0 {
		lines = append(lines, theme.Muted.Render("(0 rows)"))
	}
	for r := m.rowOff; r < m.RowCount() && len(lines) < m.height; r++ {
		cells := make([]string, len(cols))
		for i, c := range cols {
			style, text := m.look(r, c, m.rs.Rows[r][c])
			if r == m.row && c == m.col {
				if focused {
					style = style.Reverse(true)
				} else {
					style = style.Underline(true)
				}
			}
			fitted := textutil.Fit(text, m.widths[c])
			if m.words != nil && (r != m.row || c != m.col) {
				cells[i] = renderWords(style, fitted, m.words(r, c))
			} else {
				cells[i] = style.Render(fitted)
			}
		}
		lines = append(lines, strings.Join(cells, sepStyled))
	}
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for i, l := range lines {
		lines[i] = textutil.Fit(l, m.width)
	}
	return strings.Join(lines, "\n")
}

func cellText(v db.Value) string {
	if v.Null {
		return "NULL"
	}
	s := v.Text
	if len(s) > widthSample {
		s = s[:widthSample]
	}
	return textutil.Sanitize(s)
}

// Result returns the data shown (nil when empty).
func (m *Model) Result() *db.ResultSet { return m.rs }

// Row returns the row under the cursor.
func (m *Model) Row() ([]db.Value, bool) {
	if m.RowCount() == 0 {
		return nil, false
	}
	return m.rs.Rows[m.row], true
}

// ReplaceResult swaps in new data (e.g. more rows of the same query) and
// keeps the cursor where it was, clamped to the new data.
func (m *Model) ReplaceResult(rs *db.ResultSet) {
	row, col, rowOff, colOff := m.row, m.col, m.rowOff, m.colOff
	m.SetResult(rs)
	m.rowOff, m.colOff = rowOff, colOff
	m.row, m.col = row, col
	m.Move(0, 0)
	m.colOff = textutil.Clamp(m.colOff, 0, max(m.colCount()-1, 0))
}

// ScrollColumns scrolls the view n columns right (negative: left) and pulls
// the cursor back into view if it scrolled off.
func (m *Model) ScrollColumns(n int) {
	if m.colCount() == 0 {
		return
	}
	m.colOff = textutil.Clamp(m.colOff+n, 0, m.colCount()-1)
	vis := m.visibleColumns()
	if m.col < m.colOff {
		m.col = m.colOff
	}
	if last := vis[len(vis)-1]; m.col > last {
		m.col = last
	}
}

// ScrollHalf scrolls the view about half its width right (dir > 0) or left,
// at least one column.
func (m *Model) ScrollHalf(dir int) {
	if m.colCount() == 0 {
		return
	}
	start := m.colOff
	if dir < 0 {
		start--
	}
	n, used := 0, 0
	for c := start; c >= 0 && c < m.colCount(); c += dir {
		used += m.widths[c] + sepWidth
		if n > 0 && used > m.width/2 {
			break
		}
		n++
	}
	m.ScrollColumns(dir * max(n, 1))
}

// SelectColumn moves the cursor to the column called name (exactly, else
// ignoring case) and reports whether there is one.
func (m *Model) SelectColumn(name string) bool {
	if m.rs == nil {
		return false
	}
	idx := -1
	for i, c := range m.rs.Columns {
		if c.Name == name {
			idx = i
			break
		}
	}
	for i := 0; idx < 0 && i < len(m.rs.Columns); i++ {
		if strings.EqualFold(m.rs.Columns[i].Name, name) {
			idx = i
		}
	}
	if idx < 0 {
		return false
	}
	m.col = idx
	m.scrollIntoView()
	return true
}

// SelectColumnAt moves the cursor to column i and reports whether there is one.
func (m *Model) SelectColumnAt(i int) bool {
	if m.rs == nil || i < 0 || i >= len(m.rs.Columns) {
		return false
	}
	m.col = i
	m.scrollIntoView()
	return true
}

// Find moves to the next cell (row-major, wrapping) whose text contains q,
// ignoring case; backwards when forward is false. NULL cells never match.
func (m *Model) Find(q string, forward bool) bool {
	rows, cols := m.RowCount(), m.colCount()
	if q == "" || rows == 0 || cols == 0 {
		return false
	}
	q = strings.ToLower(q)
	total := rows * cols
	step := 1
	if !forward {
		step = -1
	}
	start := m.row*cols + m.col
	for i := 1; i <= total; i++ {
		k := ((start+step*i)%total + total) % total
		r, c := k/cols, k%cols
		if v := m.rs.Rows[r][c]; !v.Null && strings.Contains(strings.ToLower(v.Text), q) {
			m.row, m.col = r, c
			return true
		}
	}
	return false
}

// renderWords renders text in style with words (ignoring case) in the
// highlight color.
func renderWords(style lipgloss.Style, text string, words []string) string {
	lower := strings.ToLower(text)
	if len(words) == 0 || len(lower) != len(text) {
		return style.Render(text)
	}
	var b strings.Builder
	plain := 0
	for i := 0; i < len(text); {
		n := 0
		for _, w := range words {
			if w != "" && strings.HasPrefix(lower[i:], strings.ToLower(w)) && len(w) > n {
				n = len(w)
			}
		}
		if n == 0 {
			i++
			continue
		}
		if plain < i {
			b.WriteString(style.Render(text[plain:i]))
		}
		b.WriteString(theme.Visual.Render(text[i : i+n]))
		i += n
		plain = i
	}
	if plain < len(text) {
		b.WriteString(style.Render(text[plain:]))
	}
	return b.String()
}
