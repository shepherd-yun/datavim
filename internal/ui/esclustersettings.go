package ui

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
)

// clusterEdit is one staged cluster setting: a new value, or nil to
// remove it (back to the default).
type clusterEdit struct {
	key, scope string // scope: "persistent" or "transient"
	value      *string
}

// esClusterSettingMsg is a value typed for a cluster setting.
type esClusterSettingMsg struct {
	tabID      int
	key, scope string
	value      string
}

func (m esClusterSettingMsg) TabID() int { return m.tabID }

// esClusterSettingsRunMsg is a confirmed cluster settings change.
type esClusterSettingsRunMsg struct{ tabID int }

func (m esClusterSettingsRunMsg) TabID() int { return m.tabID }

// esClusterSettingsDoneMsg is the outcome of a cluster settings change.
type esClusterSettingsDoneMsg struct {
	tabID int
	conn  string
	err   error
}

func (m esClusterSettingsDoneMsg) TabID() int               { return m.tabID }
func (m esClusterSettingsDoneMsg) esMsg()                   {}
func (m esClusterSettingsDoneMsg) failure() (string, error) { return m.conn, m.err }

var scopeOrder = []string{"persistent", "transient", "defaults"}

// clusterRows are the Settings rows: the persistent and transient
// settings, the defaults when shown, and a row for each staged setting
// that has none yet, by scope then key.
func (t *esServerTab) clusterRows() []es.ClusterSetting {
	var out []es.ClusterSetting
	has := map[[2]string]bool{}
	for _, s := range t.csettings {
		if s.Scope == "defaults" && !t.showDefaults {
			continue
		}
		out = append(out, s)
		has[[2]string{s.Key, s.Scope}] = true
	}
	for _, e := range t.clusterEdits {
		if k := [2]string{e.key, e.scope}; !has[k] {
			has[k] = true
			out = append(out, es.ClusterSetting{Key: e.key, Scope: e.scope})
		}
	}
	slices.SortStableFunc(out, func(a, b es.ClusterSetting) int {
		return cmp.Or(slices.Index(scopeOrder, a.Scope)-slices.Index(scopeOrder, b.Scope), strings.Compare(a.Key, b.Key))
	})
	return out
}

// clusterEdit is the last staged value of key in scope.
func (t *esServerTab) clusterEdit(key, scope string) (*string, bool) {
	for i := len(t.clusterEdits) - 1; i >= 0; i-- {
		if e := t.clusterEdits[i]; e.key == key && e.scope == scope {
			return e.value, true
		}
	}
	return nil, false
}

func (t *esServerTab) clusterValue(s es.ClusterSetting) string {
	if v, ok := t.clusterEdit(s.Key, s.Scope); ok {
		if v == nil {
			return "(default)"
		}
		return *v
	}
	return s.Value
}

func (t *esServerTab) renderClusterSettings() {
	t.csView = t.clusterRows()
	rows := make([][]string, len(t.csView))
	for i, s := range t.csView {
		rows[i] = []string{s.Scope, s.Key, t.clusterValue(s)}
	}
	g := t.grids[secESSettings]
	r, c := g.Position()
	g.ReplaceResult(textResult([]string{"SCOPE", "KEY", "VALUE"}, rows))
	g.SetCursor(r, c)
}

// clusterMark marks the staged settings.
func (t *esServerTab) clusterMark(row, _ int) grid.Mark {
	if row < len(t.csView) {
		if _, ok := t.clusterEdit(t.csView[row].Key, t.csView[row].Scope); ok {
			return grid.MarkChanged
		}
	}
	return grid.MarkNone
}

func (t *esServerTab) stageCluster(key, scope string, v *string) {
	t.clusterEdits = append(t.clusterEdits, clusterEdit{key: key, scope: scope, value: v})
	t.renderClusterSettings()
}

// handleClusterSettings is a key on the Settings section: settings are
// staged like an index's and sent together.
func (t *esServerTab) handleClusterSettings(a keymap.Action) (tea.Cmd, bool) {
	r, _ := t.grids[secESSettings].Position()
	var s es.ClusterSetting
	if r < len(t.csView) {
		s = t.csView[r]
	}
	switch a {
	case keymap.ToggleInternal:
		t.showDefaults = !t.showDefaults
		t.renderClusterSettings()
		return nil, true
	case keymap.EditCell:
		if s.Key == "" {
			return nil, true
		}
		scope := s.Scope
		if scope == "defaults" {
			scope = "persistent"
		}
		id, key, value := t.id, s.Key, t.clusterValue(s)
		if value == "(default)" {
			value = ""
		}
		return openOverlay(overlay.NewPrompt(key+" ("+scope+")", "New value", value, func(v string) tea.Msg {
			return esClusterSettingMsg{tabID: id, key: key, scope: scope, value: strings.TrimSpace(v)}
		})), true
	case keymap.DeleteRow:
		if s.Key == "" || s.Scope == "defaults" {
			t.setStatus("already the default", false)
			return nil, true
		}
		t.stageCluster(s.Key, s.Scope, nil)
		return nil, true
	case keymap.UndoEdit:
		if n := len(t.clusterEdits); n > 0 {
			t.clusterEdits = t.clusterEdits[:n-1]
			t.renderClusterSettings()
		}
		return nil, true
	case keymap.UndoAllEdits:
		t.clusterEdits = nil
		t.renderClusterSettings()
		return nil, true
	case keymap.SubmitEdits:
		return t.confirmClusterSettings(), true
	}
	return nil, false
}

// changedCluster are the staged settings by scope, the last value of each.
func (t *esServerTab) changedCluster() (persistent, transient map[string]*string) {
	persistent, transient = map[string]*string{}, map[string]*string{}
	for _, e := range t.clusterEdits {
		if e.scope == "transient" {
			transient[e.key] = e.value
		} else {
			persistent[e.key] = e.value
		}
	}
	return persistent, transient
}

func (t *esServerTab) confirmClusterSettings() tea.Cmd {
	persistent, transient := t.changedCluster()
	body := es.ClusterSettingsBody(persistent, transient)
	if body == nil {
		t.setStatus("nothing to submit", false)
		return nil
	}
	var lines []string
	for _, scope := range []string{"persistent", "transient"} {
		m := persistent
		if scope == "transient" {
			m = transient
		}
		for _, k := range slices.Sorted(maps.Keys(m)) {
			v := "(default)"
			if m[k] != nil {
				v = *m[k]
			}
			lines = append(lines, scope+" "+k+": "+v)
		}
	}
	p := esPlan{
		Class: esx.Classify("PUT", "_cluster/settings", body), Count: -1, Lines: lines,
		Method: "PUT", Path: "_cluster/settings", Body: body, Expect: t.conn,
	}
	cfg, _ := t.backend.Connection(t.conn)
	return confirmESCmd(cfg, p, esClusterSettingsRunMsg{tabID: t.id}, t.setStatus)
}

func (t *esServerTab) runClusterSettings() tea.Cmd {
	persistent, transient := t.changedCluster()
	id, b, conn := t.id, t.backend, t.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		err := withES(ctx, b, conn, func(d es.Doer) error { return es.PutClusterSettings(ctx, d, persistent, transient) })
		return esClusterSettingsDoneMsg{tabID: id, conn: conn, err: err}
	}
}

// clusterUpdate handles the Settings messages; ok is false for others.
func (t *esServerTab) clusterUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case esClusterSettingMsg:
		v := m.value
		t.stageCluster(m.key, m.scope, &v)
		return nil, true
	case esClusterSettingsRunMsg:
		return t.runClusterSettings(), true
	case esClusterSettingsDoneMsg:
		if m.err != nil {
			t.setStatus(esReason(m.err), true)
			return nil, true
		}
		t.clusterEdits = nil
		cmd := t.setSection(secESSettings)
		t.setStatus("cluster settings updated", false)
		return cmd, true
	}
	return nil, false
}
