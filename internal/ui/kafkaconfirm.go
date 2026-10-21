package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

// kafkaConfirm is how every K2 write is asked for: the Cmd that opens the
// confirmation design §8.2 wants (or sends ok at once), or why it is
// refused ("" when it is not). name is what a dangerous op wants typed.
func kafkaConfirm(b Backend, conn string, op kmsgx.Op, title string, lines []string, name string, ok tea.Msg, opts ...overlay.BoxOption) (tea.Cmd, string) {
	cfg, _ := b.Connection(conn)
	if cfg.IsReadOnly() {
		return nil, readOnlyHint
	}
	prod := cfg.Env == config.EnvProd
	if prod {
		title = "PROD · " + title
	}
	switch kmsgx.ConfirmFor(op, prod) {
	case kmsgx.ConfirmNone:
		return func() tea.Msg { return ok }, ""
	case kmsgx.ConfirmTypeName:
		return openOverlay(overlay.NewConfirm(title, lines, name, ok, opts...)), ""
	}
	return openOverlay(overlay.NewConfirm(title, lines, "", ok, opts...)), ""
}
