package overlay

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// MenuItem is one entry of a Menu: its key sequence runs it.
type MenuItem struct {
	Keys  []string // as key events read; nil when unbound
	Label string   // how Keys read on screen
	Desc  string
}

// Menu is a which-key style list: an item's keys run it at once, and
// j/k + enter pick one too.
type Menu struct {
	title   string
	items   []MenuItem
	cur     int
	pending []string
	pick    func(int) tea.Msg
}

// NewMenu builds a menu; pick turns the chosen index into a message.
func NewMenu(title string, items []MenuItem, pick func(int) tea.Msg) *Menu {
	return &Menu{title: title, items: items, pick: pick}
}

func (m *Menu) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	key := msg.String()
	if len(m.pending) == 0 {
		switch key {
		case "esc":
			return nil, nil
		case "j", "down":
			m.cur = textutil.Clamp(m.cur+1, 0, len(m.items)-1)
			return m, nil
		case "k", "up":
			m.cur = textutil.Clamp(m.cur-1, 0, len(m.items)-1)
			return m, nil
		case "enter":
			return nil, emit(m.pick(m.cur))
		}
	} else if key == "esc" {
		m.pending = nil
		return m, nil
	}
	broken := len(m.pending) > 0
	switch i, res := m.match(append(m.pending, key)); res {
	case matched:
		return nil, emit(m.pick(i))
	case pending:
		return m, nil
	}
	if broken {
		// a broken sequence starts over with its last key
		return m.HandleKey(msg)
	}
	if key == "q" {
		return nil, nil
	}
	return m, nil
}

type matchResult int

const (
	noMatch matchResult = iota
	pending
	matched
)

// match looks seq up: the index of the item it completes, or it is kept
// as pending while it begins one.
func (m *Menu) match(seq []string) (int, matchResult) {
	m.pending = nil
	for i, it := range m.items {
		if slices.Equal(it.Keys, seq) && len(seq) > 0 {
			return i, matched
		}
		if len(it.Keys) > len(seq) && slices.Equal(it.Keys[:len(seq)], seq) {
			m.pending = seq
		}
	}
	if m.pending != nil {
		return 0, pending
	}
	return 0, noMatch
}

func (m *Menu) View(width, _ int) string {
	keyW := 0
	for _, it := range m.items {
		keyW = max(keyW, ansi.StringWidth(it.Label))
	}
	lines := make([]string, 0, len(m.items)+2)
	for i, it := range m.items {
		pad := strings.Repeat(" ", keyW-ansi.StringWidth(it.Label))
		if i == m.cur {
			lines = append(lines, theme.Selected.Render(fmt.Sprintf("> %s%s  %s", it.Label, pad, it.Desc)))
			continue
		}
		lines = append(lines, "  "+theme.ActiveTab.Render(it.Label)+pad+"  "+it.Desc)
	}
	footer := "key runs it · j/k enter · esc cancel"
	if len(m.pending) > 0 {
		var typed strings.Builder
		for _, k := range m.pending {
			if len([]rune(k)) > 1 {
				k = "<" + k + ">"
			}
			typed.WriteString(k)
		}
		footer = typed.String() + "… · esc cancel"
	}
	lines = append(lines, "", theme.Muted.Render(footer))
	return theme.Box(m.title, strings.Join(lines, "\n"), max(min(width-4, 50), 20), len(lines)+2, true)
}
