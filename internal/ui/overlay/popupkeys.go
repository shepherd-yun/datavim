package overlay

import (
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
)

// popupHint is "<keys> label" for the popup key bound to a, or "" when
// keymap.toml left it unbound.
func popupHint(k *keymap.Keymap, a keymap.Action, label string) string {
	keys := k.OwnKey(keymap.Popup, a)
	if keys == "" {
		return ""
	}
	return keys + " " + label
}

// footer joins the non-empty parts with " · ".
func footer(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}
