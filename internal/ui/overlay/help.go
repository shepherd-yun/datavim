package overlay

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// Help lists the key bindings of the focused area, with the global keys
// used most; tab flips to a page of every global key.
type Help struct {
	pages  [2][]string // the area's keys; every global key
	page   int
	offset int
}

// NewHelp groups entries by context: the area's own and the essential
// global ones first, all global ones on the second page.
func NewHelp(entries []keymap.HelpEntry) *Help {
	var main, global []keymap.HelpEntry
	for _, e := range entries {
		if e.Context != keymap.Global {
			main = append(main, e)
			continue
		}
		global = append(global, e)
		if e.Essential() {
			main = append(main, e)
		}
	}
	h := &Help{pages: [2][]string{helpLines(main), helpLines(global)}}
	if len(global) == 0 || len(main) == len(entries) {
		h.pages[1] = nil // nothing more to show
	}
	return h
}

func helpLines(entries []keymap.HelpEntry) []string {
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
	return lines
}

func (h *Help) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "?":
		return nil, nil
	case "tab", "shift+tab":
		if h.pages[1] != nil {
			h.page, h.offset = 1-h.page, 0
		}
	case "j", "down":
		h.offset++
	case "k", "up":
		h.offset--
	}
	h.offset = textutil.Clamp(h.offset, 0, len(h.pages[h.page])-1)
	return h, nil
}

func (h *Help) View(width, height int) string {
	title := "Help · j/k scroll · esc close"
	switch {
	case h.pages[1] == nil:
	case h.page == 0:
		title = "Help · tab all global keys · j/k scroll · esc close"
	default:
		title = "Help: global keys · tab back · j/k scroll · esc close"
	}
	return scrollBox(title, h.pages[h.page], &h.offset, width, height, 70)
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
