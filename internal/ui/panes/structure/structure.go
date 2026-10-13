// Package structure shows a table's columns, indexes, foreign keys and DDL.
package structure

import (
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
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
	section       Section
	offset        int
	width, height int
}

func New() *Model { return &Model{} }

func (m *Model) SetData(d Data)   { m.data, m.offset = d, 0 }
func (m *Model) SetSize(w, h int) { m.width, m.height = max(w, 0), max(h, 0) }
func (m *Model) Section() Section { return m.section }
func (m *Model) NextSection()     { m.section, m.offset = (m.section+1)%sectionCount, 0 }
func (m *Model) PrevSection()     { m.section, m.offset = (m.section+sectionCount-1)%sectionCount, 0 }
func (m *Model) Top()             { m.offset = 0 }
func (m *Model) Bottom()          { m.offset = m.maxOffset() }
func (m *Model) HalfPage(dir int) { m.Scroll(dir * max(m.bodyHeight()/2, 1)) }
func (m *Model) Scroll(delta int) { m.offset = textutil.Clamp(m.offset+delta, 0, m.maxOffset()) }
func (m *Model) bodyHeight() int  { return max(m.height-2, 0) }
func (m *Model) maxOffset() int   { return max(len(m.lines())-m.bodyHeight(), 0) }

func (m *Model) lines() []string {
	switch m.section {
	case Columns:
		rows := make([][]string, len(m.data.Columns))
		for i, c := range m.data.Columns {
			key, null, def := "", "NOT NULL", ""
			if c.PrimaryKey {
				key = "PK"
			}
			if c.Nullable {
				null = "NULL"
			}
			if c.Default != nil {
				def = *c.Default
			}
			rows[i] = []string{key, c.Name, c.Type, null, def, c.Comment}
		}
		return textutil.Table([]string{"", "Name", "Type", "Null", "Default", "Comment"}, rows)
	case Indexes:
		rows := make([][]string, len(m.data.Indexes))
		for i, idx := range m.data.Indexes {
			kind := "INDEX"
			switch {
			case idx.Primary:
				kind = "PRIMARY"
			case idx.Unique:
				kind = "UNIQUE"
			}
			rows[i] = []string{idx.Name, kind, strings.Join(idx.Columns, ", ")}
		}
		return textutil.Table([]string{"Name", "Kind", "Columns"}, rows)
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
		return textutil.Table([]string{"Name", "Columns", "References"}, rows)
	default:
		return strings.Split(m.data.DDL, "\n")
	}
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
		out = append(out, textutil.Fit(textutil.Sanitize(l), m.width))
	}
	if len(out) > m.height {
		out = out[:m.height]
	}
	return strings.Join(out, "\n")
}
