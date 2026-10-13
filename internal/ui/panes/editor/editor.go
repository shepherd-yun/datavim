// Package editor is the console's SQL editor. A Bubbles textarea edits the
// text (NORMAL mode only moves the cursor, INSERT mode gets keys); View
// draws it with syntax colors, soft wrapping and a block cursor.
package editor

import (
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/highlight"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
)

// taWidth is the textarea's own width. View does its own wrapping; this
// only keeps the textarea from wrapping lines internally, so j and k move
// by whole lines (lines longer than this still wrap inside the textarea).
const taWidth = 1024

// Model wraps a textarea.
type Model struct {
	ta            textarea.Model
	insert        bool
	width, height int
	top           int // first visible screen row

	highlight func(string) []highlight.Span
	hlText    string
	hlClasses []highlight.Class
	curX      int // where View drew the cursor
	curY      int
}

// New returns an empty editor in NORMAL mode.
func New() *Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.MaxHeight = 9999
	ta.SetWidth(taWidth)
	st := ta.Styles()
	st.Cursor.Blink = false // View draws its own cursor; no blink ticks
	ta.SetStyles(st)
	_ = ta.Focus() // always focused; insert decides who gets keys
	return &Model{ta: ta}
}

// SetSize sets the drawing area.
func (m *Model) SetSize(w, h int) {
	m.width, m.height = max(w, 0), max(h, 0)
	m.ta.SetHeight(max(h, 1))
}

// SetHighlighter sets how View colors the text (nil: no colors).
func (m *Model) SetHighlighter(f func(string) []highlight.Span) {
	m.highlight, m.hlClasses = f, nil
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

// Cursor returns the cursor line and column (in runes), both 0-based.
func (m *Model) Cursor() (line, col int) { return m.ta.Line(), m.ta.Column() }

// SetCursor moves the cursor to line and column (in runes), clamped.
func (m *Model) SetCursor(line, col int) {
	line = textutil.Clamp(line, 0, m.ta.LineCount()-1)
	m.ta.MoveToBegin()
	// CursorDown moves by screen row, so a line the textarea wraps takes
	// several steps.
	for i := 0; m.ta.Line() < line && i < 1<<20; i++ {
		m.ta.CursorDown()
	}
	m.ta.SetCursorColumn(col)
}

// SetCursorOffset moves the cursor to a byte offset into Value().
func (m *Model) SetCursorOffset(off int) {
	v := m.ta.Value()
	before := v[:textutil.Clamp(off, 0, len(v))]
	line := strings.Count(before, "\n")
	col := utf8.RuneCountInString(before[strings.LastIndexByte(before, '\n')+1:])
	m.SetCursor(line, col)
}

// ReplaceBeforeCursor replaces the text from byte offset start to the
// cursor with s and leaves the cursor after s.
func (m *Model) ReplaceBeforeCursor(start int, s string) {
	v, cur := m.ta.Value(), m.Offset()
	start = textutil.Clamp(start, 0, cur)
	m.ta.SetValue(v[:start] + s + v[cur:])
	m.SetCursorOffset(start + len(s))
}

// CursorPos is where the last View drew the cursor, relative to the
// editor's top-left corner (line numbers included).
func (m *Model) CursorPos() (x, y int) { return m.curX, m.curY }
