package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/editor"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
	"github.com/shepherd-yun/sqlvim/internal/ui/vimedit"
)

// CellEditor edits one cell value with the vim editor. It opens in INSERT
// mode at the end of the value; enter saves, ctrl+n saves NULL, and esc
// goes to NORMAL mode and, there, cancels.
type CellEditor struct {
	title  string
	ed     *editor.Model
	start  int // the editor's version when it opened
	submit func(db.Value) tea.Msg
}

// NewCellEditor opens an editor on v; submit turns the saved value into a
// message.
func NewCellEditor(title string, v db.Value, submit func(db.Value) tea.Msg) *CellEditor {
	ed := editor.New()
	ed.SetPlaceholder("")
	if !v.Null {
		ed.SetValue(v.Text)
	}
	ed.AppendAtEnd()
	return &CellEditor{title: title, ed: ed, start: ed.Version(), submit: submit}
}

func (c *CellEditor) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if c.ed.Version() == c.start {
			return nil, nil // nothing was edited: the cell stays as it was
		}
		return nil, emit(c.submit(db.Value{Text: c.ed.Value()}))
	case "ctrl+n":
		return nil, emit(c.submit(db.Value{Null: true}))
	case "esc":
		if c.ed.Mode() == vimedit.Normal && c.ed.Pending() == "" {
			return nil, nil
		}
	}
	c.ed.Update(msg) // the "+ register and gt / gT mean nothing here
	return c, nil
}

// Mode is the editor's vim mode, for the status bar.
func (c *CellEditor) Mode() string { return c.ed.Mode().String() }

// Paste types a terminal paste in INSERT mode.
func (c *CellEditor) Paste(text string) { c.ed.Update(tea.PasteMsg{Content: text}) }

func (c *CellEditor) View(width, height int) string {
	w := max(min(width-4, 80), 24)
	lines := strings.Count(c.ed.Value(), "\n") + 1
	edH := textutil.Clamp(lines, 3, max(height-8, 3))
	c.ed.SetSize(w-2, edH)
	esc := "esc normal mode"
	if c.ed.Mode() == vimedit.Normal {
		esc = "esc cancel"
	}
	body := c.ed.View() + "\n\n" + theme.Muted.Render("enter save · ctrl+n NULL · "+esc)
	return theme.Box(c.title+" · "+c.Mode(), body, w, edH+4, true)
}
