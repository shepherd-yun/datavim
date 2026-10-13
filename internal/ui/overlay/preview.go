package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
)

// Preview shows statements before they run: y runs them, n or esc
// cancels, j / k and ctrl+d / ctrl+u scroll.
type Preview struct {
	title  string
	lines  []string
	ok     tea.Msg
	offset int
}

// NewPreview builds the preview; y emits ok.
func NewPreview(title string, lines []string, ok tea.Msg) *Preview {
	return &Preview{title: title, lines: lines, ok: ok}
}

func (p *Preview) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		return nil, emit(p.ok)
	case "n", "N", "esc", "q":
		return nil, nil
	case "j", "down":
		p.offset++
	case "k", "up":
		p.offset--
	case "ctrl+d":
		p.offset += 10
	case "ctrl+u":
		p.offset -= 10
	}
	return p, nil
}

func (p *Preview) View(width, height int) string {
	inner := max(min(width-4, 120), 10) - 2
	var lines []string
	for _, l := range p.lines {
		for _, part := range strings.Split(l, "\n") {
			lines = append(lines, textutil.Wrap(textutil.Sanitize(part), inner)...)
		}
	}
	return scrollBox(p.title+" · y submit · n cancel", lines, &p.offset, width, height, 120)
}
