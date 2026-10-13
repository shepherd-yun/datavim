// Package editor is the console's SQL editor: a Bubbles textarea with a
// NORMAL mode (cursor moves only) and an INSERT mode (textarea gets keys).
package editor

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
)

// Model wraps a textarea.
type Model struct {
	ta     textarea.Model
	insert bool
	height int
}

// New returns an empty editor in NORMAL mode.
func New() *Model {
	ta := textarea.New()
	ta.ShowLineNumbers = true
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.MaxHeight = 9999
	ta.Placeholder = "i to write SQL · enter runs the statement under the cursor"
	st := ta.Styles()
	st.Cursor.Blink = false
	ta.SetStyles(st)
	_ = ta.Focus() // always focused so the cursor is drawn; insert decides who gets keys
	return &Model{ta: ta}
}

// SetSize sets the drawing area.
func (m *Model) SetSize(w, h int) {
	m.height = max(h, 0)
	m.ta.SetWidth(max(w, 1))
	m.ta.SetHeight(max(h, 1))
}

func (m *Model) Value() string { return m.ta.Value() }

// SetValue replaces the text and puts the cursor at the end.
func (m *Model) SetValue(s string) { m.ta.SetValue(s) }

// InsertString inserts s at the cursor.
func (m *Model) InsertString(s string) { m.ta.InsertString(s) }

func (m *Model) Insert() bool { return m.insert }
func (m *Model) EnterInsert() { m.insert = true }
func (m *Model) ExitInsert()  { m.insert = false }

// Update forwards key presses and pastes to the textarea in INSERT mode.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	if !m.insert {
		return nil
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return cmd
}

func (m *Model) Up(n int) {
	for range n {
		m.ta.CursorUp()
	}
}

func (m *Model) Down(n int) {
	for range n {
		m.ta.CursorDown()
	}
}

func (m *Model) Left()      { m.ta.SetCursorColumn(m.ta.Column() - 1) }
func (m *Model) Right()     { m.ta.SetCursorColumn(m.ta.Column() + 1) }
func (m *Model) LineStart() { m.ta.CursorStart() }
func (m *Model) LineEnd()   { m.ta.CursorEnd() }
func (m *Model) Top()       { m.ta.MoveToBegin() }
func (m *Model) Bottom()    { m.ta.MoveToEnd() }

// HalfPage moves half the visible height in direction dir (+1 / -1).
func (m *Model) HalfPage(dir int) {
	n := max(m.height/2, 1)
	if dir > 0 {
		m.Down(n)
	} else {
		m.Up(n)
	}
}

// Offset is the cursor position as a byte offset into Value().
func (m *Model) Offset() int {
	lines := strings.Split(m.ta.Value(), "\n")
	row := textutil.Clamp(m.ta.Line(), 0, len(lines)-1)
	off := 0
	for _, l := range lines[:row] {
		off += len(l) + 1
	}
	r := []rune(lines[row])
	return off + len(string(r[:textutil.Clamp(m.ta.Column(), 0, len(r))]))
}

func (m *Model) View() string {
	if m.height == 0 {
		return ""
	}
	return m.ta.View()
}
