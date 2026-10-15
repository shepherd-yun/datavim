// Package structure shows a table's columns, indexes, foreign keys and DDL.
package structure

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/schemaedit"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/highlight"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// Data is everything the view shows.
type Data struct {
	Columns     []db.Column
	Indexes     []db.Index
	ForeignKeys []db.ForeignKey
	DDL         string
}

// Section is one sub-page.
type Section int

const (
	Columns Section = iota
	Indexes
	ForeignKeys
	DDL
	sectionCount
)

var sectionNames = [...]string{"Columns", "Indexes", "Foreign Keys", "DDL"}

// Model is the structure view state.
type Model struct {
	data          Data
	edit          *schemaedit.Table // the table being modified; nil shows data
	section       Section
	offset        int
	row, field    int // the cursor in Columns, Indexes and Foreign Keys; field only while editing
	width, height int
	dialect       sqlx.Dialect
	ddl           []string // highlighted DDL lines
}

// New returns an empty view; d selects the DDL highlighting.
func New(d sqlx.Dialect) *Model { return &Model{dialect: d} }

// SetData replaces what the view shows.
func (m *Model) SetData(d Data) {
	m.data, m.offset = d, 0
	clean := textutil.SanitizeText(d.DDL)
	m.ddl = theme.HighlightLines(clean, highlight.SQL(clean, m.dialect))
	m.clampRow()
}

// SetEdit shows e's staged columns and indexes and lets the cursor pick a
// field; nil goes back to the table as read.
func (m *Model) SetEdit(e *schemaedit.Table) {
	m.edit, m.field = e, 0
	m.clampRow()
}

func (m *Model) Editing() bool            { return m.edit != nil }
func (m *Model) Cursor() (row, field int) { return m.row, m.field }
func (m *Model) SetSize(w, h int)         { m.width, m.height = max(w, 0), max(h, 0); m.follow() }
func (m *Model) Section() Section         { return m.section }
func (m *Model) NextSection()             { m.SetCursor((m.section+1)%sectionCount, 0) }
func (m *Model) PrevSection()             { m.SetCursor((m.section+sectionCount-1)%sectionCount, 0) }
func (m *Model) bodyHeight() int          { return max(m.height-2, 0) }
func (m *Model) maxOffset() int           { return max(len(m.lines())-m.bodyHeight(), 0) }

// SetCursor puts the cursor on row of section s.
func (m *Model) SetCursor(s Section, row int) {
	if s != m.section {
		m.section, m.field, m.offset = s, 0, 0
	}
	m.row = row
	m.clampRow()
}

// SetField puts the cursor on field f of its row.
func (m *Model) SetField(f int) { m.field = textutil.Clamp(f, 0, m.fieldCount()-1) }

// MoveField moves the cursor between a row's fields while editing.
func (m *Model) MoveField(delta int) { m.SetField(m.field + delta) }

// CursorName is the name of the column, index or foreign key under the
// cursor, or the whole DDL on the DDL page; "" when there is none.
func (m *Model) CursorName() string {
	switch m.section {
	case Columns:
		if m.edit != nil {
			if rows := m.edit.Columns(); m.row < len(rows) {
				return rows[m.row].Col.Name
			}
		} else if m.row < len(m.data.Columns) {
			return m.data.Columns[m.row].Name
		}
	case Indexes:
		if m.edit != nil {
			if rows := m.edit.Indexes(); m.row < len(rows) {
				return rows[m.row].Index.Name
			}
		} else if m.row < len(m.data.Indexes) {
			return m.data.Indexes[m.row].Name
		}
	case ForeignKeys:
		if m.row < len(m.data.ForeignKeys) {
			return m.data.ForeignKeys[m.row].Name
		}
	case DDL:
		return m.data.DDL
	}
	return ""
}

// Find returns the row of the column (Columns) or index (Indexes) called
// name: among the staged rows while editing, else among the rows read.
func (m *Model) Find(s Section, name string) (int, bool) {
	var names []string
	switch {
	case s == Columns && m.edit != nil:
		for _, r := range m.edit.Columns() {
			names = append(names, r.Col.Name)
		}
	case s == Columns:
		for _, c := range m.data.Columns {
			names = append(names, c.Name)
		}
	case s == Indexes && m.edit != nil:
		for _, r := range m.edit.Indexes() {
			names = append(names, r.Index.Name)
		}
	case s == Indexes:
		for _, ix := range m.data.Indexes {
			names = append(names, ix.Name)
		}
	}
	i := slices.Index(names, name)
	return i, i >= 0
}

func (m *Model) fieldCount() int {
	switch {
	case m.edit == nil:
		return 1
	case m.section == Columns:
		return int(schemaedit.ColumnFields)
	case m.section == Indexes:
		return int(schemaedit.IndexFields)
	}
	return 1
}

func (m *Model) rowCount() int {
	switch m.section {
	case Columns:
		if m.edit != nil {
			return len(m.edit.Columns())
		}
		return len(m.data.Columns)
	case Indexes:
		if m.edit != nil {
			return len(m.edit.Indexes())
		}
		return len(m.data.Indexes)
	case ForeignKeys:
		return len(m.data.ForeignKeys)
	}
	return 0
}

func (m *Model) clampRow() {
	m.row = textutil.Clamp(m.row, 0, m.rowCount()-1)
	m.field = textutil.Clamp(m.field, 0, m.fieldCount()-1)
	m.follow()
}

// follow scrolls so that the cursor's row shows; the top row brings the
// header back.
func (m *Model) follow() {
	if m.section == DDL {
		return
	}
	line := m.row + 2 // below the header and its rule
	switch h := m.bodyHeight(); {
	case m.row == 0:
		m.offset = 0
	case line < m.offset:
		m.offset = line
	case h > 0 && line >= m.offset+h:
		m.offset = line - h + 1
	}
}

// Scroll moves the cursor delta rows; the DDL scrolls instead.
func (m *Model) Scroll(delta int) {
	if m.section == DDL {
		m.offset = textutil.Clamp(m.offset+delta, 0, m.maxOffset())
		return
	}
	m.row += delta
	m.clampRow()
}

func (m *Model) Top()             { m.Scroll(-max(m.rowCount(), len(m.ddl))) }
func (m *Model) Bottom()          { m.Scroll(max(m.rowCount(), len(m.ddl))) }
func (m *Model) HalfPage(dir int) { m.Scroll(dir * max(m.bodyHeight()/2, 1)) }

func (m *Model) lines() []string {
	switch m.section {
	case Columns:
		if m.edit != nil {
			return m.editColumns()
		}
		rows := make([][]string, len(m.data.Columns))
		for i, c := range m.data.Columns {
			rows[i] = []string{keyMark(c.PrimaryKey), c.Name, c.Type, nullText(c.Nullable), defaultText(c.Default), c.Comment}
		}
		return table(columnHeaders, rows, m.rowStyle)
	case Indexes:
		if m.edit != nil {
			return m.editIndexes()
		}
		rows := make([][]string, len(m.data.Indexes))
		for i, idx := range m.data.Indexes {
			rows[i] = []string{idx.Name, indexKind(idx), strings.Join(idx.Columns, ", ")}
		}
		return table(indexHeaders, rows, m.rowStyle)
	case ForeignKeys:
		rows := make([][]string, len(m.data.ForeignKeys))
		for i, fk := range m.data.ForeignKeys {
			ref := fk.RefTable.Name
			if ns := fk.RefTable.Schema; ns != "" {
				ref = ns + "." + ref
			} else if ns := fk.RefTable.Database; ns != "" {
				ref = ns + "." + ref
			}
			rows[i] = []string{fk.Name, strings.Join(fk.Columns, ", "), ref + "(" + strings.Join(fk.RefColumns, ", ") + ")"}
		}
		return table([]string{"Name", "Columns", "References"}, rows, m.rowStyle)
	default:
		return m.ddl
	}
}

var (
	columnHeaders = []string{"", "Name", "Type", "Null", "Default", "Comment"}
	indexHeaders  = []string{"Name", "Kind", "Columns"}
)

func keyMark(pk bool) string {
	if pk {
		return "PK"
	}
	return ""
}

func nullText(nullable bool) string {
	if nullable {
		return "NULL"
	}
	return "NOT NULL"
}

func defaultText(d *string) string {
	if d == nil {
		return ""
	}
	return *d
}

func indexKind(idx db.Index) string {
	switch {
	case idx.Primary:
		return "PRIMARY"
	case idx.Unique:
		return "UNIQUE"
	}
	return "INDEX"
}

// rowStyle highlights the cursor's row of the table as read.
func (m *Model) rowStyle(row, _ int) (lipgloss.Style, bool) {
	return theme.Selected, row == m.row
}

// cellStyle marks a field being modified: the cursor, then deleted, new
// and changed rows and fields, colored like staged data edits.
func cellStyle(state schemaedit.State, changed, cursor bool) (lipgloss.Style, bool) {
	switch {
	case cursor:
		return theme.Selected, true
	case state == schemaedit.Deleted:
		return theme.Deleted, true
	case state == schemaedit.Added:
		return theme.Inserted, true
	case changed:
		return theme.Changed, true
	}
	return lipgloss.Style{}, false
}

func (m *Model) editColumns() []string {
	cols := m.edit.Columns()
	rows := make([][]string, len(cols))
	for i, r := range cols {
		rows[i] = []string{keyMark(r.Col.PrimaryKey)}
		for f := range schemaedit.ColumnFields {
			rows[i] = append(rows[i], r.Text(f))
		}
	}
	return table(columnHeaders, rows, func(row, col int) (lipgloss.Style, bool) {
		r := cols[row]
		if col == 0 { // the key mark is not a field
			return cellStyle(r.State(), false, false)
		}
		f := schemaedit.ColumnField(col - 1)
		return cellStyle(r.State(), r.Changed(f), row == m.row && col-1 == m.field)
	})
}

func (m *Model) editIndexes() []string {
	idx := m.edit.Indexes()
	rows := make([][]string, len(idx))
	for i, r := range idx {
		for f := range schemaedit.IndexFields {
			rows[i] = append(rows[i], r.Text(f))
		}
	}
	return table(indexHeaders, rows, func(row, col int) (lipgloss.Style, bool) {
		r := idx[row]
		return cellStyle(r.State(), r.Changed(schemaedit.IndexField(col)), row == m.row && col == m.field)
	})
}

// View renders the section header and the visible body lines.
func (m *Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	tabs := make([]string, len(sectionNames))
	for i, name := range sectionNames {
		if Section(i) == m.section {
			tabs[i] = theme.ActiveTab.Render("[" + name + "]")
		} else {
			tabs[i] = theme.Muted.Render(" " + name + " ")
		}
	}
	out := []string{textutil.Fit(strings.Join(tabs, " ")+theme.Muted.Render("  [ ] switch"), m.width), ""}

	lines := m.lines()
	m.offset = textutil.Clamp(m.offset, 0, m.maxOffset())
	end := min(m.offset+m.bodyHeight(), len(lines))
	for _, l := range lines[m.offset:end] {
		out = append(out, textutil.Fit(l, m.width))
	}
	if len(out) > m.height {
		out = out[:m.height]
	}
	return strings.Join(out, "\n")
}
