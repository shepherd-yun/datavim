package overlay

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// WhichKey renders the pending-sequence hint box.
func WhichKey(hints []keymap.Hint) string {
	lines := make([]string, len(hints))
	for i, h := range hints {
		lines[i] = theme.ActiveTab.Render(h.Keys) + "  " + h.Desc
	}
	body := strings.Join(lines, "\n")
	return theme.Box("keys", body, lipgloss.Width(body)+4, len(lines)+2, true)
}
