package overlay

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// Choice picks one of a few options.
type Choice struct {
	title   string
	options []string
	cur     int
	pick    func(int) tea.Msg
}

// NewChoice builds a chooser; pick turns the chosen index into a message.
func NewChoice(title string, options []string, pick func(int) tea.Msg) *Choice {
	return &Choice{title: title, options: options, pick: pick}
}

func (c *Choice) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch key := msg.String(); key {
	case "esc", "q":
		return nil, nil
	case "j", "down":
		c.cur = textutil.Clamp(c.cur+1, 0, len(c.options)-1)
	case "k", "up":
		c.cur = textutil.Clamp(c.cur-1, 0, len(c.options)-1)
	case "enter":
		return nil, emit(c.pick(c.cur))
	default:
		if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= len(c.options) {
			return nil, emit(c.pick(n - 1))
		}
	}
	return c, nil
}

func (c *Choice) View(width, _ int) string {
	lines := make([]string, 0, len(c.options)+2)
	for i, o := range c.options {
		line := "  " + o
		if i == c.cur {
			line = theme.Selected.Render("> " + o)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", theme.Muted.Render("j/k move · enter choose · esc cancel"))
	return theme.Box(c.title, strings.Join(lines, "\n"), max(min(width-4, 50), 20), len(lines)+2, true)
}
