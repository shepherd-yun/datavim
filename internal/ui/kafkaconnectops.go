package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
)

// handleOps runs the connector writes; ok is false for other actions.
func (t *connectorTab) handleOps(keymap.Action) (tea.Cmd, bool) { return nil, false }
