package ui

import (
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
)

// keyHint is "<keys> label" for the keys that trigger a in ctx, or "" when
// keymap.toml left a unbound there.
func keyHint(k *keymap.Keymap, ctx keymap.Context, a keymap.Action, label string) string {
	keys := k.KeysFor(ctx, a)
	if keys == "" {
		return ""
	}
	return keys + " " + label
}

// joinHints joins the non-empty hints with " · ".
func joinHints(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}
