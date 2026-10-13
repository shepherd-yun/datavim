package overlay

import (
	"bytes"
	"encoding/json"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
)

// Detail shows one cell value in full; JSON is pretty-printed.
type Detail struct {
	title  string
	text   string
	offset int
}

// NewDetail builds the popup for a cell.
func NewDetail(title, text string) *Detail {
	return &Detail{title: title, text: prettyJSON(text)}
}

func prettyJSON(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") && !strings.HasPrefix(t, "[") || !json.Valid([]byte(t)) {
		return s
	}
	var b bytes.Buffer
	if err := json.Indent(&b, []byte(t), "", "  "); err != nil {
		return s
	}
	return b.String()
}

func (d *Detail) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "K":
		return nil, nil
	case "j", "down":
		d.offset++
	case "k", "up":
		d.offset--
	}
	return d, nil
}

func (d *Detail) View(width, height int) string {
	inner := max(min(width-4, 100), 10) - 2
	var lines []string
	for _, l := range strings.Split(d.text, "\n") {
		lines = append(lines, textutil.Wrap(textutil.Sanitize(l), inner)...)
	}
	return scrollBox(d.title+" · esc close", lines, &d.offset, width, height, 100)
}
