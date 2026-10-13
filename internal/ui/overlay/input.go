package overlay

import (
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// field is a minimal single-line text input (append-only cursor).
type field struct{ r []rune }

func (f *field) handle(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "backspace":
		if len(f.r) > 0 {
			f.r = f.r[:len(f.r)-1]
		}
	case "ctrl+u":
		f.r = nil
	default:
		if msg.Text != "" && !msg.Mod.Contains(tea.ModCtrl) {
			f.r = append(f.r, []rune(msg.Text)...)
		}
	}
}

// paste appends a terminal paste. The field is single-line, so newlines and
// other control characters are dropped (password managers often copy a
// trailing newline).
func (f *field) paste(s string) {
	for _, r := range s {
		if !unicode.IsControl(r) {
			f.r = append(f.r, r)
		}
	}
}

func (f *field) String() string { return string(f.r) }
func (f *field) Len() int       { return len(f.r) }

func newField(s string) field { return field{r: []rune(s)} }
