package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// Secret asks for a hidden value, such as an SSH key passphrase.
type Secret struct {
	title, label string
	input        field
	submit       func(string) tea.Msg
}

// NewSecret builds the prompt; submit turns the typed value into a message.
func NewSecret(title, label string, submit func(string) tea.Msg) *Secret {
	return &Secret{title: title, label: label, submit: submit}
}

func (s *Secret) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "enter":
		return nil, emit(s.submit(s.input.String()))
	}
	s.input.handle(msg)
	return s, nil
}

func (s *Secret) View(width, _ int) string {
	w := max(min(width-4, 70), 20)
	body := textutil.Wrap(textutil.Sanitize(s.label), w-2)
	body = append(body,
		"> "+strings.Repeat("•", s.input.Len())+"█",
		"",
		theme.Muted.Render("enter ok · esc cancel"),
	)
	return theme.Box(s.title, strings.Join(body, "\n"), w, len(body)+2, true)
}

// Paste appends text to the input.
func (s *Secret) Paste(text string) { s.input.paste(text) }
