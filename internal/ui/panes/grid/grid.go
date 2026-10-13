// Package grid renders a result set as a navigable table.
package grid

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

const (
	sep      = " │ "
	sepWidth = 3
	// widthSample bounds the bytes measured per cell so huge values stay cheap.
	widthSample = 1024
)

// Model is a table with a cell cursor.
type Model struct {
	rs             *db.ResultSet
	maxColumnWidth int
	widths         []int
	row, col       int
	rowOff, colOff int
	width, height  int
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
	m.widths = nil
	if rs == nil {
		return
	}
	m.widths = make([]int, len(rs.Columns))
	for c, col := range rs.Columns {
		w := ansi.StringWidth(textutil.Sanitize(col.Name))
		for _, r := range rs.Rows {
			w = max(w, ansi.StringWidth(cellText(r[c])))
		}
		m.widths[c] = textutil.Clamp(w, 1, m.maxColumnWidth)
	}
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
		header[i] = theme.Header.Render(textutil.Fit(textutil.Sanitize(m.rs.Columns[c].Name), m.widths[c]))
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
			v := m.rs.Rows[r][c]
			style := lipgloss.NewStyle()
			if v.Null {
				style = theme.Null
			}
			if r == m.row && c == m.col {
				if focused {
					style = style.Reverse(true)
				} else {
					style = style.Underline(true)
				}
			}
			cells[i] = style.Render(textutil.Fit(cellText(v), m.widths[c]))
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
