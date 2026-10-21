package ui

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

type (
	// cfgEditedMsg is a config value typed in the cell editor.
	cfgEditedMsg struct {
		tabID       int
		name, value string
	}
	// cfgApplyMsg is the confirmed config changes.
	cfgApplyMsg struct {
		tabID   int
		changes []kafka.ConfigChange
	}
	// cfgAppliedMsg is the outcome of AlterTopicConfigs.
	cfgAppliedMsg struct {
		tabID int
		conn  string
		n     int
		err   error
	}
)

func (m cfgEditedMsg) TabID() int                { return m.tabID }
func (m cfgApplyMsg) TabID() int                 { return m.tabID }
func (m cfgAppliedMsg) TabID() int               { return m.tabID }
func (m cfgAppliedMsg) kafkaMsg()                {}
func (m cfgAppliedMsg) failure() (string, error) { return m.conn, m.err }

// staged is the last staged edit of config name.
func (t *topicTab) staged(name string) (kmsgx.ConfigEdit, bool) {
	for i := len(t.cfgEdits) - 1; i >= 0; i-- {
		if t.cfgEdits[i].Name == name {
			return t.cfgEdits[i], true
		}
	}
	return kmsgx.ConfigEdit{}, false
}

// changes counts the configs with a staged edit.
func (t *topicTab) changes() int {
	seen := map[string]bool{}
	for _, e := range t.cfgEdits {
		seen[e.Name] = true
	}
	return len(seen)
}

// unsavedHint is "<space>w applies · U discards" with the keymap's keys.
func (t *topicTab) unsavedHint(submit, discard string) string {
	return joinHints(keyHint(t.keys, keymap.KafkaConfig, keymap.SubmitEdits, submit),
		keyHint(t.keys, keymap.KafkaConfig, keymap.UndoAllEdits, discard))
}

// cfgNote is "2 changes · <space>w applies" while configs are staged.
func (t *topicTab) cfgNote() string {
	n := t.changes()
	if n == 0 {
		return ""
	}
	return theme.Accent.Render(plural(n, "change")) + theme.Muted.Render(" · "+
		keyHint(t.keys, keymap.KafkaConfig, keymap.SubmitEdits, "applies"))
}

func (t *topicTab) configResult() *db.ResultSet {
	if t.configs == nil {
		return nil
	}
	rows := make([][]string, len(t.configs))
	for i, c := range t.configs {
		v := c.Value
		if c.Sensitive {
			v = "******"
		}
		if e, ok := t.staged(c.Name); ok && !e.Delete {
			v = e.Value
		}
		rows[i] = []string{c.Name, v, c.Source}
	}
	return textResult([]string{"NAME", "VALUE", "SOURCE"}, rows)
}

// cfgMark shows staged configs like staged cells (a new value, or back
// to the default as a deleted one) and the others set away from their
// default.
func (t *topicTab) cfgMark(row, _ int) grid.Mark {
	if row >= len(t.configs) {
		return grid.MarkNone
	}
	c := t.configs[row]
	if e, ok := t.staged(c.Name); ok {
		if e.Delete {
			return grid.MarkDeleted
		}
		return grid.MarkChanged
	}
	if !c.Default {
		return grid.MarkSet
	}
	return grid.MarkNone
}

func (t *topicTab) showConfigEdits() {
	t.cfgGrid.ReplaceResult(t.configResult())
}

// handleConfig is the Config section's editing keys.
func (t *topicTab) handleConfig(a keymap.Action) (tea.Cmd, bool) {
	switch a {
	case keymap.EditCell, keymap.DeleteRow:
		r, _ := t.cfgGrid.Position()
		if r >= len(t.configs) {
			return nil, true
		}
		c := t.configs[r]
		if c.Sensitive {
			t.setStatus(kmsgx.ErrSensitiveConfig.Error(), true)
			return nil, true
		}
		if a == keymap.DeleteRow {
			t.cfgEdits = append(t.cfgEdits, kmsgx.ConfigEdit{Name: c.Name, Delete: true})
			t.showConfigEdits()
			return nil, true
		}
		v := c.Value
		if e, ok := t.staged(c.Name); ok && !e.Delete {
			v = e.Value
		}
		id, name := t.id, c.Name
		return openOverlay(overlay.NewCellEditor(name, db.Value{Text: v}, func(nv db.Value) tea.Msg {
			return cfgEditedMsg{tabID: id, name: name, value: nv.Text}
		})), true
	case keymap.UndoEdit:
		if n := len(t.cfgEdits); n > 0 {
			t.cfgEdits = t.cfgEdits[:n-1]
			t.showConfigEdits()
		}
		return nil, true
	case keymap.UndoAllEdits:
		t.cfgEdits = nil
		t.showConfigEdits()
		return nil, true
	case keymap.SubmitEdits:
		return t.submit(), true
	}
	return nil, false
}

// submit asks to apply the staged configs (design §8.2).
func (t *topicTab) submit() tea.Cmd {
	changes, err := kmsgx.PlanConfigs(t.configs, t.cfgEdits)
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	if len(changes) == 0 {
		t.setStatus("no changes", false)
		return nil
	}
	lines := []string{fmt.Sprintf("Apply %s to %s?", plural(len(changes), "config change"), t.topic)}
	for _, c := range changes {
		to := c.Value
		if c.Delete {
			to = "(default)"
		}
		old := c.Old
		if c.OldDefault {
			old += " (default)"
		}
		lines = append(lines, c.Name+"  "+old+" → "+to)
	}
	cmd, why := kafkaConfirm(t.backend, t.conn, kmsgx.OpAlterConfigs, "Topic configs", lines, t.topic,
		cfgApplyMsg{tabID: t.id, changes: changes})
	if why != "" {
		t.setStatus(why, true)
	}
	return cmd
}

func (t *topicTab) applyConfigs(changes []kafka.ConfigChange) tea.Cmd {
	t.setStatus("applying "+plural(len(changes), "config change")+"…", false)
	tabCtx, id, b, conn, topic := t.ctx, t.id, t.backend, t.conn, t.topic
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, queryTimeout)
		defer cancel()
		err := withKafka(ctx, b, conn, func(c kafka.Client) error {
			return c.AlterTopicConfigs(ctx, topic, changes)
		})
		return cfgAppliedMsg{tabID: id, conn: conn, n: len(changes), err: err}
	}
}

// configsUpdate handles the Config section's messages.
func (t *topicTab) configsUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case cfgEditedMsg:
		t.cfgEdits = append(t.cfgEdits, kmsgx.ConfigEdit{Name: m.name, Value: m.value})
		t.showConfigEdits()
		return nil, true
	case cfgApplyMsg:
		return t.applyConfigs(m.changes), true
	case cfgAppliedMsg:
		if m.err != nil {
			// the staged edits stay; ErrConfigChanged wants the new values shown
			t.setStatus(kafkaErr(m.err), true)
			if errors.Is(m.err, kafka.ErrConfigChanged) {
				return t.loadDetail(), true
			}
			return nil, true
		}
		t.cfgEdits = nil
		t.setStatus("applied "+plural(m.n, "config change"), false)
		return t.loadDetail(), true
	}
	return nil, false
}
