package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// Prompt asks for one line of text with an editable initial value.
type Prompt struct {
	title, label string
	input        field
	submit       func(string) tea.Msg
}

// NewPrompt builds a prompt; submit turns the entered text into a message.
func NewPrompt(title, label, initial string, submit func(string) tea.Msg) *Prompt {
	return &Prompt{title: title, label: label, input: newField(initial), submit: submit}
}

func (p *Prompt) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "enter":
		return nil, emit(p.submit(p.input.String()))
	}
	p.input.handle(msg)
	return p, nil
}

func (p *Prompt) View(width, _ int) string {
	w := max(min(width-4, 80), 20)
	body := strings.Join([]string{
		p.label,
		"> " + p.input.View(w-4),
		"",
		theme.Muted.Render("enter ok · esc cancel"),
	}, "\n")
	return theme.Box(p.title, body, w, 6, true)
}

// Paste appends text to the input.
func (p *Prompt) Paste(text string) { p.input.paste(text) }
