package overlay

import tea "charm.land/bubbletea/v2"

// CommandMsg is an entered ":" command without the colon.
type CommandMsg struct{ Text string }

// CmdLine is the vim-style ":" command line drawn in the status bar.
type CmdLine struct{ input field }

// NewCmdLine opens an empty command line.
func NewCmdLine() *CmdLine { return &CmdLine{} }

func (c *CmdLine) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "enter":
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
func (c *CmdLine) View(_, _ int) string { return ":" + c.input.String() + "█" }
