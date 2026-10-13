package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// PasswordSubmittedMsg carries a password the user typed.
type PasswordSubmittedMsg struct {
	Conn     string
	Password string
	Save     bool
}

// Password prompts for a connection password.
type Password struct {
	conn  string
	input field
	save  bool
}

// NewPassword builds a prompt; saving to the keychain is on by default.
func NewPassword(conn string) *Password { return &Password{conn: conn, save: true} }

func (p *Password) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "enter":
		return nil, emit(PasswordSubmittedMsg{Conn: p.conn, Password: p.input.String(), Save: p.save})
	case "tab":
		p.save = !p.save
	default:
		p.input.handle(msg)
	}
	return p, nil
}

func (p *Password) View(width, _ int) string {
	check := "[ ]"
	if p.save {
		check = "[x]"
	}
	body := strings.Join([]string{
		"Password for " + theme.Title.Render(p.conn) + ":",
		"> " + strings.Repeat("•", p.input.Len()) + "█",
		"",
		check + " save to Keychain (tab)",
		theme.Muted.Render("enter connect · esc cancel"),
	}, "\n")
	return theme.Box("Connect", body, max(min(width-4, 50), 10), 7, true)
}
