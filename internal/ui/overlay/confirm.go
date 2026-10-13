package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// Confirm asks before something risky. With expect set the user must type it
// exactly; otherwise y confirms. On success it emits ok.
type Confirm struct {
	title  string
	lines  []string
	expect string
	ok     tea.Msg
	input  field
	wrong  bool
}

// NewConfirm builds a confirmation box.
func NewConfirm(title string, lines []string, expect string, ok tea.Msg) *Confirm {
	return &Confirm{title: title, lines: lines, expect: expect, ok: ok}
}

func (c *Confirm) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	key := msg.String()
	if key == "esc" {
		return nil, nil
	}
	if c.expect == "" {
		switch key {
		case "y", "Y":
			return nil, emit(c.ok)
		case "n", "N":
			return nil, nil
		}
		return c, nil
	}
	if key == "enter" {
		if c.input.String() == c.expect {
			return nil, emit(c.ok)
		}
		c.wrong = true
		return c, nil
	}
	c.wrong = false
	c.input.handle(msg)
	return c, nil
}

func (c *Confirm) View(width, height int) string {
	w := max(min(width-4, 80), 20)
	var body []string
	for _, l := range c.lines {
		for _, part := range strings.Split(l, "\n") {
			body = append(body, textutil.Wrap(textutil.Sanitize(part), w-2)...)
		}
	}
	body = append(body, "")
	if c.expect == "" {
		body = append(body, theme.Muted.Render("y confirm · n/esc cancel"))
	} else {
		body = append(body, "Type "+theme.Title.Render(textutil.Sanitize(c.expect))+" to continue:",
			"> "+c.input.String()+"█")
		if c.wrong {
			body = append(body, theme.Error.Render("does not match"))
		}
		body = append(body, theme.Muted.Render("enter confirm · esc cancel"))
	}
	h := min(len(body)+2, max(height-2, 3))
	return theme.Box(c.title, strings.Join(body, "\n"), w, h, true)
}

// Paste appends text to the input.
func (c *Confirm) Paste(text string) {
	c.wrong = false
	c.input.paste(text)
}
