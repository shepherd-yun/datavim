package overlay

import tea "charm.land/bubbletea/v2"

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

func (f *field) String() string { return string(f.r) }
func (f *field) Len() int       { return len(f.r) }
