package ui

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
)

// esAliasNameMsg is the name typed for a new alias.
type esAliasNameMsg struct {
	tabID int
	name  string
}

func (m esAliasNameMsg) TabID() int { return m.tabID }

// esAliasesMsg asks to confirm alias actions; esAliasRunMsg runs them.
type (
	esAliasesMsg struct {
		tabID int
		acts  []es.AliasAction
	}
	esAliasRunMsg struct {
		tabID int
		acts  []es.AliasAction
	}
)

func (m esAliasesMsg) TabID() int  { return m.tabID }
func (m esAliasRunMsg) TabID() int { return m.tabID }

// esSettingMsg is a setting's new value from the prompt.
type esSettingMsg struct {
	tabID      int
	key, value string
}

func (m esSettingMsg) TabID() int { return m.tabID }

// esSettingsRunMsg sends the staged settings, confirmed.
type esSettingsRunMsg struct{ tabID int }

func (m esSettingsRunMsg) TabID() int { return m.tabID }

// esIndexWriteMsg is an Aliases or Settings write's outcome.
type esIndexWriteMsg struct {
	tabID   int
	conn    string
	section indexSection
	done    string
	err     error
}

func (m esIndexWriteMsg) TabID() int               { return m.tabID }
func (m esIndexWriteMsg) esMsg()                   {}
func (m esIndexWriteMsg) failure() (string, error) { return m.conn, m.err }

// settingEdit is one staged setting: a new value, or nil for the default.
type settingEdit struct {
	key   string
	value *string
}

// handleAliases is a key on the Aliases section.
func (t *esIndexTab) handleAliases(a keymap.Action) (tea.Cmd, bool) {
	switch a {
	case keymap.InsertRow:
		id := t.id
		return openOverlay(overlay.NewPrompt("Add an alias to "+t.target, "Alias name", "", func(s string) tea.Msg {
			return esAliasNameMsg{tabID: id, name: strings.TrimSpace(s)}
		})), true
	case keymap.DeleteRow:
		r, _ := t.aliasGrid.Position()
		if r >= len(t.aliases) {
			return nil, true
		}
		al := t.aliases[r]
		return t.confirmAliases([]es.AliasAction{{Index: al.Index, Alias: al.Name}}), true
	}
	return nil, false
}

// aliasNamed asks whether the new alias is the write index.
func (t *esIndexTab) aliasNamed(m esAliasNameMsg) tea.Cmd {
	if m.name == "" {
		return nil
	}
	var indices []string
	for _, i := range t.indices {
		indices = append(indices, i.Name)
	}
	if len(indices) == 0 {
		indices = []string{t.target}
	}
	id, name := t.id, m.name
	return openOverlay(overlay.NewChoice("Is "+name+" the write index?", []string{"no", "yes: writes through " + name + " go to " + indices[0]},
		func(i int) tea.Msg {
			var acts []es.AliasAction
			for n, idx := range indices {
				act := es.AliasAction{Add: true, Index: idx, Alias: name}
				if i == 1 {
					w := n == 0
					act.IsWrite = &w
				}
				acts = append(acts, act)
			}
			return esAliasesMsg{tabID: id, acts: acts}
		}))
}

func aliasBody(acts []es.AliasAction) []byte {
	var out []map[string]map[string]any
	for _, a := range acts {
		op := "remove"
		if a.Add {
			op = "add"
		}
		out = append(out, map[string]map[string]any{op: {"index": a.Index, "alias": a.Alias}})
	}
	b, _ := json.Marshal(map[string]any{"actions": out})
	return b
}

func (t *esIndexTab) confirmAliases(acts []es.AliasAction) tea.Cmd {
	var lines []string
	for _, a := range acts {
		if a.Add {
			lines = append(lines, "add alias "+a.Alias+" → "+a.Index)
		} else {
			lines = append(lines, "remove alias "+a.Alias+" → "+a.Index)
		}
	}
	body := aliasBody(acts)
	p := esPlan{Class: esx.Classify("POST", "_aliases", body), Lines: lines, Method: "POST", Path: "_aliases", Body: body}
	cfg, _ := t.backend.Connection(t.conn)
	return confirmESCmd(cfg, p, esAliasRunMsg{tabID: t.id, acts: acts}, t.setStatus)
}

func (t *esIndexTab) write(section indexSection, done string, fn func(ctx context.Context, d es.Doer) error) tea.Cmd {
	b, conn, id, tabCtx := t.backend, t.conn, t.id, t.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		out := esIndexWriteMsg{tabID: id, conn: conn, section: section, done: done}
		d, err := b.ES(ctx, conn)
		if err == nil {
			err = fn(ctx, d)
		}
		out.err = err
		return out
	}
}

// handleSettings is a key on the Settings section: the dynamic settings
// are edited like a topic's configs and sent together.
func (t *esIndexTab) handleSettings(a keymap.Action) (tea.Cmd, bool) {
	r, _ := t.setGrid.Position()
	var s es.Setting
	if r < len(t.settingList) {
		s = t.settingList[r]
	}
	switch a {
	case keymap.EditCell:
		switch {
		case s.Key == "":
			return nil, true
		case !s.Dynamic:
			t.setStatus("static setting; needs a closed index", true)
			return nil, true
		}
		id, key := t.id, s.Key
		return openOverlay(overlay.NewPrompt(key, "New value", t.settingValue(s), func(v string) tea.Msg {
			return esSettingMsg{tabID: id, key: key, value: strings.TrimSpace(v)}
		})), true
	case keymap.DeleteRow:
		if s.Key == "" || s.Default {
			t.setStatus("already the default", false)
			return nil, true
		}
		t.stageSetting(s.Key, nil)
		return nil, true
	case keymap.UndoEdit:
		if n := len(t.setEdits); n > 0 {
			t.setEdits = t.setEdits[:n-1]
			t.setGrid.ReplaceResult(t.settingsResult())
		}
		return nil, true
	case keymap.UndoAllEdits:
		t.setEdits = nil
		t.setGrid.ReplaceResult(t.settingsResult())
		return nil, true
	case keymap.SubmitEdits:
		return t.confirmSettings(), true
	}
	return nil, false
}

// settingValue is s with its staged edit.
func (t *esIndexTab) settingValue(s es.Setting) string {
	if e, ok := t.settingEdit(s.Key); ok {
		if e == nil {
			return "(default)"
		}
		return *e
	}
	return s.Value
}

// settingEdit is key's last staged value; ok is false when unstaged.
func (t *esIndexTab) settingEdit(key string) (*string, bool) {
	for i := len(t.setEdits) - 1; i >= 0; i-- {
		if t.setEdits[i].key == key {
			return t.setEdits[i].value, true
		}
	}
	return nil, false
}

func (t *esIndexTab) stageSetting(key string, v *string) {
	t.setEdits = append(t.setEdits, settingEdit{key: key, value: v})
	r, c := t.setGrid.Position()
	t.setGrid.ReplaceResult(t.settingsResult())
	t.setGrid.SetCursor(r, c)
}

// changedSettings are the staged settings, the last value of each.
func (t *esIndexTab) changedSettings() map[string]*string {
	out := map[string]*string{}
	for _, e := range t.setEdits {
		out[e.key] = e.value
	}
	return out
}

func (t *esIndexTab) confirmSettings() tea.Cmd {
	changed := t.changedSettings()
	if len(changed) == 0 {
		t.setStatus("nothing to submit", false)
		return nil
	}
	keys := make([]string, 0, len(changed))
	for k := range changed {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var lines []string
	for _, k := range keys {
		v := "(default)"
		if changed[k] != nil {
			v = *changed[k]
		}
		lines = append(lines, k+": "+v)
	}
	body, _ := json.Marshal(changed)
	path := t.target + "/_settings"
	p := esPlan{Class: esx.Classify("PUT", path, body), Lines: lines, Method: "PUT", Path: path, Body: body, Expect: t.target}
	cfg, _ := t.backend.Connection(t.conn)
	return confirmESCmd(cfg, p, esSettingsRunMsg{tabID: t.id}, t.setStatus)
}

// settingsMark marks the staged settings as changed, the rest set away
// from their defaults as set.
func (t *esIndexTab) settingsMark(row, col int) grid.Mark {
	if row < len(t.settingList) {
		if _, ok := t.settingEdit(t.settingList[row].Key); ok {
			return grid.MarkChanged
		}
	}
	return t.settingMark(row, col)
}

// editUpdate handles the Aliases and Settings messages; ok is false for
// other messages.
func (t *esIndexTab) editUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case esAliasNameMsg:
		return t.aliasNamed(m), true
	case esAliasesMsg:
		return t.confirmAliases(m.acts), true
	case esAliasRunMsg:
		acts := m.acts
		return t.write(idxAliases, "aliases updated", func(ctx context.Context, d es.Doer) error {
			return es.UpdateAliases(ctx, d, acts)
		}), true
	case esMappingRunMsg:
		return t.runMapping(m), true
	case esSettingMsg:
		v := m.value
		t.stageSetting(m.key, &v)
		return nil, true
	case esSettingsRunMsg:
		changed, target := t.changedSettings(), t.target
		return t.write(idxSettings, "settings updated", func(ctx context.Context, d es.Doer) error {
			return es.PutSettings(ctx, d, target, changed)
		}), true
	case esIndexWriteMsg:
		if m.err != nil {
			t.setStatus(esReason(m.err), true)
			return nil, true
		}
		t.setStatus(m.done, false)
		switch m.section {
		case idxSettings:
			t.setEdits = nil
		case idxMapping:
			return tea.Batch(t.loadHead(), t.loadMapping()), true
		}
		return t.loadSection(m.section), true
	}
	return nil, false
}
