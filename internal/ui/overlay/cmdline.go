package overlay

import tea "charm.land/bubbletea/v2"

// CommandMsg is an entered ":" command without the colon.
type CommandMsg struct{ Text string }

// SearchMsg is an entered "/" search without the slash.
type SearchMsg struct{ Text string }

// CmdLine is the vim-style ":" command line or "/" search line drawn in the status bar.
type CmdLine struct {
	prefix string
	input  field
}

// NewCmdLine opens an empty command line.
func NewCmdLine() *CmdLine { return &CmdLine{prefix: ":"} }

// NewSearchLine opens an empty search line.
func NewSearchLine() *CmdLine { return &CmdLine{prefix: "/"} }

func (c *CmdLine) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "enter":
		if c.prefix == "/" {
			return nil, emit(SearchMsg{Text: c.input.String()})
		}
		return nil, emit(CommandMsg{Text: c.input.String()})
	case "backspace":
		if c.input.Len() == 0 {
			return nil, nil
		}
	}
	c.input.handle(msg)
	return c, nil
}

// View renders the single status-bar line.
func (c *CmdLine) View(_, _ int) string { return c.prefix + c.input.String() + "█" }

// Paste appends text to the input.
func (c *CmdLine) Paste(text string) { c.input.paste(text) }
