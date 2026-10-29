package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// PasswordSubmittedMsg carries a password the user typed.
type PasswordSubmittedMsg struct {
	Conn     string
	Password string
	Save     bool
}

// Password prompts for a connection password.
type Password struct {
	conn   string
	input  field
	save   bool
	envVar string // set: the keychain is unavailable, and this variable can hold the password
}

// NewPassword builds a prompt; saving to the keychain is on by default.
func NewPassword(conn string) *Password { return &Password{conn: conn, save: true} }

// WithoutKeychain is the prompt when the keychain cannot be used: the
// password is kept for this run, and envVar is named as the way to set it
// for good.
func (p *Password) WithoutKeychain(envVar string) *Password {
	p.save, p.envVar = false, envVar
	return p
}

func (p *Password) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "enter":
		return nil, emit(PasswordSubmittedMsg{Conn: p.conn, Password: p.input.String(), Save: p.save})
	case "tab":
		p.save = !p.save && p.envVar == ""
	default:
		p.input.handle(msg)
	}
	return p, nil
}

func (p *Password) View(width, _ int) string {
	w := max(min(width-4, 50), 10)
	check := "[ ] save to Keychain (tab)"
	if p.save {
		check = "[✓] save to Keychain (tab)"
	}
	lines := []string{
		"Password for " + theme.Title.Render(p.conn) + ":",
		"> " + p.input.ViewMasked(w-4, '•'),
		"",
		check,
	}
	if p.envVar != "" {
		lines = append(lines[:3], theme.Muted.Render("keychain unavailable: kept for this run"), theme.Muted.Render("(or set "+p.envVar+")"))
	}
	lines = append(lines, theme.Muted.Render("enter connect · esc cancel"))
	return theme.Box("Connect", strings.Join(lines, "\n"), w, len(lines)+2, true)
}

// Paste inserts text at the cursor.
func (p *Password) Paste(text string) { p.input.paste(text) }
