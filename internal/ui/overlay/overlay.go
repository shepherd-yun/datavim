// Package overlay draws popups (help, prompts, cell detail) over the main UI.
package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Overlay is a modal popup. HandleKey returns a literal nil Overlay to close.
type Overlay interface {
	HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd)
	View(width, height int) string
}

// Paster is an overlay with a text input that accepts a terminal paste
// (bracketed paste arrives as one tea.PasteMsg, not as key presses).
type Paster interface {
	Paste(text string)
}

const reset = "\x1b[0m"

// PlaceAt draws box over base with its top-left corner at (x, y).
func PlaceAt(base, box string, x, y int) string {
	lines := strings.Split(base, "\n")
	for i, bl := range strings.Split(box, "\n") {
		row := y + i
		if row < 0 || row >= len(lines) {
			continue
		}
		line := lines[row]
		left := ansi.Truncate(line, x, "")
		if pad := x - ansi.StringWidth(left); pad > 0 {
			left += strings.Repeat(" ", pad)
		}
		right := ansi.TruncateLeft(line, x+ansi.StringWidth(bl), "")
		lines[row] = left + reset + bl + reset + right
	}
	return strings.Join(lines, "\n")
}

// Center draws box in the middle of a width x height base.
func Center(base, box string, width, height int) string {
	x := max((width-lipgloss.Width(box))/2, 0)
	y := max((height-lipgloss.Height(box))/2, 0)
	return PlaceAt(base, box, x, y)
}

func emit(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }
