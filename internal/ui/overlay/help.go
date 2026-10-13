package overlay

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// Help lists the key bindings of the focused context.
type Help struct {
	lines  []string
	offset int
}

// NewHelp groups entries by context.
func NewHelp(entries []keymap.HelpEntry) *Help {
	var lines []string
	var last keymap.Context
	for _, e := range entries {
		if e.Context != last {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, theme.Header.Render(string(e.Context)))
			last = e.Context
		}
		lines = append(lines, fmt.Sprintf("  %-16s %s", e.Keys, e.Desc))
	}
	return &Help{lines: lines}
}

func (h *Help) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "?":
		return nil, nil
	case "j", "down":
		h.offset++
	case "k", "up":
		h.offset--
	}
	h.offset = textutil.Clamp(h.offset, 0, len(h.lines)-1)
	return h, nil
}

func (h *Help) View(width, height int) string {
	return scrollBox("Help · j/k scroll · esc close", h.lines, &h.offset, width, height, 70)
}

// scrollBox frames lines[offset:] in a box sized to fit within width x height.
func scrollBox(title string, lines []string, offset *int, width, height, maxWidth int) string {
	w := max(min(width-4, maxWidth), 10)
	h := max(min(height-2, len(lines)+2), 3)
	*offset = textutil.Clamp(*offset, 0, max(len(lines)-(h-2), 0))
	end := min(*offset+h-2, len(lines))
	body := ""
	for i, l := range lines[*offset:end] {
		if i > 0 {
			body += "\n"
		}
		body += l
	}
	return theme.Box(title, body, w, h, true)
}
