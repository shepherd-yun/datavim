package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// Picker is a filterable list; typing narrows it.
type Picker struct {
	title   string
	items   []string
	filter  field
	matches []int
	cur     int
	offset  int
	pick    func(int) tea.Msg
}

// NewPicker builds a picker; pick receives the index into items.
func NewPicker(title string, items []string, pick func(index int) tea.Msg) *Picker {
	p := &Picker{title: title, items: items, pick: pick}
	p.refilter()
	return p
}

func (p *Picker) refilter() {
	q := strings.ToLower(p.filter.String())
	p.matches = p.matches[:0]
	for i, it := range p.items {
		if strings.Contains(strings.ToLower(it), q) {
			p.matches = append(p.matches, i)
		}
	}
	p.cur, p.offset = 0, 0
}

func (p *Picker) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "enter":
		if len(p.matches) == 0 {
			return p, nil
		}
		return nil, emit(p.pick(p.matches[p.cur]))
	case "down", "ctrl+n", "ctrl+j":
		p.cur = textutil.Clamp(p.cur+1, 0, len(p.matches)-1)
	case "up", "ctrl+p", "ctrl+k":
		p.cur = textutil.Clamp(p.cur-1, 0, len(p.matches)-1)
	default:
		before := p.filter.String()
		p.filter.handle(msg)
		if p.filter.String() != before {
			p.refilter()
		}
	}
	return p, nil
}

func (p *Picker) View(width, height int) string {
	w := max(min(width-4, 100), 20)
	h := max(min(height-2, 22), 5)
	listH := h - 4 // border, filter line, footer
	if p.cur < p.offset {
		p.offset = p.cur
	}
	if p.cur >= p.offset+listH {
		p.offset = p.cur - listH + 1
	}
	lines := []string{"> " + textutil.Sanitize(p.filter.String()) + "█"}
	if len(p.matches) == 0 {
		lines = append(lines, theme.Muted.Render("(no matches)"))
	}
	for i := p.offset; i < len(p.matches) && i < p.offset+listH; i++ {
		label := textutil.Fit(textutil.Sanitize(p.items[p.matches[i]]), w-2)
		if i == p.cur {
			label = theme.Selected.Render(label)
		}
		lines = append(lines, label)
	}
	body := textutil.PadLines(strings.Join(lines, "\n"), h-3) + "\n" +
		theme.Muted.Render("type to filter · ↑/↓ move · enter pick · esc close")
	return theme.Box(p.title, body, w, h, true)
}

// Paste appends text to the filter.
func (p *Picker) Paste(text string) {
	p.filter.paste(text)
	p.refilter()
}
