// Package editor is the console's SQL editor: vimedit edits the text and
// View draws it with syntax colors, the visual selection, soft wrapping
// and a block cursor.
package editor

import (
	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/highlight"
	"github.com/shepherd-yun/sqlvim/internal/ui/vimedit"
)

// Model is the editor pane.
type Model struct {
	vim           *vimedit.Editor
	width, height int
	top           int // first visible screen row

	highlight func(string) []highlight.Span
	hlText    string
	hlClasses []highlight.Class
	curX      int // where View drew the cursor
	curY      int

	placeholder string
	blurred     bool // draw the cursor and current line number as inactive
}

// New returns an empty editor in NORMAL mode.
func New() *Model { return &Model{vim: vimedit.New(), placeholder: defaultPlaceholder} }

// SetPlaceholder sets the hint shown in an empty editor ("" for none).
func (m *Model) SetPlaceholder(s string) { m.placeholder = s }

// SetFocused sets whether the editor has the keyboard: without it the
// cursor is underlined instead of a solid block, as in the results grid.
func (m *Model) SetFocused(on bool) { m.blurred = !on }

// AppendAtEnd starts INSERT mode after the last character (vim's GA).
func (m *Model) AppendAtEnd() {
	m.Bottom()
	m.vim.Feed(vimedit.Key{Name: "A", Text: "A"})
}

// SetSize sets the drawing area.
func (m *Model) SetSize(w, h int) {
	m.width, m.height = max(w, 0), max(h, 0)
	m.vim.SetHeight(h)
}

// SetHighlighter sets how View colors the text (nil: no colors).
func (m *Model) SetHighlighter(f func(string) []highlight.Span) {
	m.highlight, m.hlClasses = f, nil
}

// KeyOf turns a Bubble Tea key press into a vimedit key.
func KeyOf(msg tea.KeyPressMsg) vimedit.Key {
	k := vimedit.Key{Name: msg.String()}
	if key := msg.Key(); key.Text != "" && key.Mod&^tea.ModShift == 0 {
		k.Text = key.Text
	}
	return k
}

// Update feeds a key press or a terminal paste (in any mode) to the
// editor. Handled is false for keys the editor leaves to the console.
func (m *Model) Update(msg tea.Msg) vimedit.Result {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.vim.Feed(KeyOf(msg))
	case tea.PasteMsg:
		m.vim.Paste(msg.Content)
		return vimedit.Result{Handled: true}
	}
	return vimedit.Result{}
}

func (m *Model) Value() string { return m.vim.Text() }

// SetValue replaces the text as newly loaded (u does not undo it); the
// cursor goes to the end.
func (m *Model) SetValue(s string) { m.vim.SetText(s) }

// ReplaceAll replaces the text as one change that u undoes.
func (m *Model) ReplaceAll(s string) { m.vim.ReplaceAll(s) }

// Version changes whenever the text does.
func (m *Model) Version() int { return m.vim.Version() }

func (m *Model) Mode() vimedit.Mode { return m.vim.Mode() }
func (m *Model) Insert() bool       { return m.vim.Mode() == vimedit.Insert }

// Visual reports VISUAL and V-LINE mode.
func (m *Model) Visual() bool {
	md := m.vim.Mode()
	return md == vimedit.Visual || md == vimedit.VisualLine
}

// EnterInsert is i; Escape is esc (it leaves INSERT or VISUAL mode).
func (m *Model) EnterInsert() { m.vim.Feed(vimedit.Key{Name: "i", Text: "i"}) }
func (m *Model) Escape()      { m.vim.Feed(vimedit.Key{Name: "esc"}) }

// Pending is the command typed so far (vim's showcmd).
func (m *Model) Pending() string { return m.vim.Pending() }

// Selection is the visual selection as byte offsets into Value().
func (m *Model) Selection() (from, to int, ok bool) { return m.vim.Selection() }

// PasteClipboard finishes a "+p with the clipboard's text.
func (m *Model) PasteClipboard(p vimedit.ClipboardPaste, text string) bool {
	return m.vim.PasteClipboard(p, text)
}

// Offset is the cursor position as a byte offset into Value().
func (m *Model) Offset() int { return m.vim.Offset() }

// Cursor returns the cursor line and column (in runes), both 0-based.
func (m *Model) Cursor() (line, col int) {
	p := m.vim.Cursor()
	return p.Line, p.Col
}

// SetCursor moves the cursor to line and column (in runes), clamped.
func (m *Model) SetCursor(line, col int) { m.vim.SetCursor(vimedit.Pos{Line: line, Col: col}) }

// SetCursorOffset moves the cursor to a byte offset into Value().
func (m *Model) SetCursorOffset(off int) { m.vim.SetOffset(off) }

// ReplaceBeforeCursor replaces the text from byte offset start to the
// cursor with s and leaves the cursor after s.
func (m *Model) ReplaceBeforeCursor(start int, s string) { m.vim.ReplaceBeforeCursor(start, s) }

func (m *Model) Top()    { m.SetCursor(0, 0) }
func (m *Model) Bottom() { m.SetCursor(1<<30, 1<<30) }

// CursorPos is where the last View drew the cursor, relative to the
// editor's top-left corner (line numbers included).
func (m *Model) CursorPos() (x, y int) { return m.curX, m.curY }
