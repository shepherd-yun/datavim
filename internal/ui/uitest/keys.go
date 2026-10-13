// Package uitest builds Bubble Tea key events for tests.
package uitest

import (
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

var special = map[string]tea.KeyPressMsg{
	"enter":      {Code: tea.KeyEnter},
	"ctrl+enter": {Code: tea.KeyEnter, Mod: tea.ModCtrl},
	"esc":        {Code: tea.KeyEscape},
	"tab":        {Code: tea.KeyTab},
	"shift+tab":  {Code: tea.KeyTab, Mod: tea.ModShift},
	"backspace":  {Code: tea.KeyBackspace},
	"space":      {Code: tea.KeySpace, Text: " "},
	"up":         {Code: tea.KeyUp},
	"down":       {Code: tea.KeyDown},
	"left":       {Code: tea.KeyLeft},
	"right":      {Code: tea.KeyRight},
}

// Key builds a key press whose String() is s, e.g. "j", "G", "ctrl+d", "enter".
func Key(s string) tea.KeyPressMsg {
	if k, ok := special[s]; ok {
		return k
	}
	if rest, ok := strings.CutPrefix(s, "ctrl+"); ok {
		r, _ := utf8.DecodeRuneInString(rest)
		return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
	}
	r, _ := utf8.DecodeRuneInString(s)
	return tea.KeyPressMsg{Code: r, Text: s}
}

// Type returns one key press per rune of s.
func Type(s string) []tea.KeyPressMsg {
	var out []tea.KeyPressMsg
	for _, r := range s {
		out = append(out, Key(string(r)))
	}
	return out
}
