package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/kmsgx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

// connectorRecheck is how long after a pause, resume or restart the tab
// reads the connector once more: its state takes a moment to change.
const connectorRecheck = 3 * time.Second

type (
	// connectorOpMsg is a confirmed pause, resume or restart; task is the
	// task of OpRestartTask.
	connectorOpMsg struct {
		tabID int
		op    kmsgx.Op
		task  int
	}
	// connectorOpDoneMsg is the outcome of a connector write.
	connectorOpDoneMsg struct {
		tabID  int
		conn   string
		status string
		err    error
	}
	connectorRecheckMsg struct{ tabID int }
	// connectorValidatedMsg is the cluster's verdict on an edited config.
	connectorValidatedMsg struct {
		tabID    int
		conn     string
		old, cfg map[string]string
		problems []kafka.ConfigProblem
		err      error
	}
	// connectorSetMsg is a confirmed config edit.
	connectorSetMsg struct {
		tabID    int
		old, cfg map[string]string
	}
)

func (m connectorOpMsg) TabID() int        { return m.tabID }
func (m connectorOpDoneMsg) TabID() int    { return m.tabID }
func (m connectorRecheckMsg) TabID() int   { return m.tabID }
func (m connectorValidatedMsg) TabID() int { return m.tabID }
func (m connectorSetMsg) TabID() int       { return m.tabID }

// handleOps runs the connector writes; ok is false for other actions.
func (t *connectorTab) handleOps(a keymap.Action) (tea.Cmd, bool) {
	if !t.loaded {
		return nil, a == keymap.PauseResume || a == keymap.RestartConnector || a == keymap.ExternalEdit
	}
	switch a {
	case keymap.PauseResume:
		if t.c.State == "PAUSED" || t.c.State == "STOPPED" {
			return t.confirm(kmsgx.OpResumeConnector, "Resume connector", fmt.Sprintf("Resume %s?", t.name), 0), true
		}
		return t.confirm(kmsgx.OpPauseConnector, "Pause connector",
			fmt.Sprintf("Pause %s? Its tasks stop until you resume it.", t.name), 0), true
	case keymap.RestartConnector:
		if t.section == secCTasks {
			r, _ := t.grids[secCTasks].Position()
			if r < 0 || r >= len(t.c.Tasks) {
				return nil, true
			}
			id := t.c.Tasks[r].ID
			return t.confirm(kmsgx.OpRestartTask, "Restart task", fmt.Sprintf("Restart task %d of %s?", id, t.name), id), true
		}
		return t.confirm(kmsgx.OpRestartConnector, "Restart connector", fmt.Sprintf("Restart %s and its failed tasks?", t.name), 0), true
	case keymap.ExternalEdit:
		return t.editConfig(), true
	}
	return nil, false
}

func (t *connectorTab) confirm(op kmsgx.Op, title, line string, task int) tea.Cmd {
	cmd, why := kafkaConfirm(t.backend, t.conn, op, title, []string{line}, t.name, connectorOpMsg{tabID: t.id, op: op, task: task})
	if why != "" {
		t.setStatus(why, true)
	}
	return cmd
}

// write runs fn on the connector's cluster; a lost connection is not retried.
func (t *connectorTab) write(done string, fn func(context.Context, kafka.Connect) error) tea.Cmd {
	id, b, conn, cluster := t.id, t.backend, t.conn, t.cluster
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := connectorOpDoneMsg{tabID: id, conn: conn, status: done}
		c, err := b.KafkaConnect(ctx, conn, cluster)
		if err == nil {
			err = fn(ctx, c)
		}
		msg.err = err
		return msg
	}
}

func (t *connectorTab) runOp(m connectorOpMsg) tea.Cmd {
	name := t.name
	switch m.op {
	case kmsgx.OpPauseConnector:
		return t.write("paused "+name, func(ctx context.Context, c kafka.Connect) error { return c.Pause(ctx, name) })
	case kmsgx.OpResumeConnector:
		return t.write("resumed "+name, func(ctx context.Context, c kafka.Connect) error { return c.Resume(ctx, name) })
	case kmsgx.OpRestartConnector:
		return t.write("restarting "+name, func(ctx context.Context, c kafka.Connect) error { return c.Restart(ctx, name) })
	case kmsgx.OpRestartTask:
		return t.write(fmt.Sprintf("restarting task %d", m.task), func(ctx context.Context, c kafka.Connect) error {
			return c.RestartTask(ctx, name, m.task)
		})
	}
	return nil
}

// editableConfig is the config as the temp file shows it: sensitive
// values masked, keys sorted, two-space indents.
func editableConfig(cfg map[string]string) string {
	shown := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if sensitiveConnectKey(k) {
			v = maskedValue
		}
		shown[k] = v
	}
	b, _ := json.MarshalIndent(shown, "", "  ") // map keys come out sorted
	return string(b) + "\n"
}

func (t *connectorTab) editConfig() tea.Cmd {
	if cfg, _ := t.backend.Connection(t.conn); cfg.IsReadOnly() {
		t.setStatus(readOnlyHint, true)
		return nil
	}
	t.editOld = maps.Clone(t.c.Config)
	return writeTempEdit(t.id, editableConfig(t.editOld), "datavim-"+safeFileName(t.name)+"-*.json")
}

// parseEditedConfig reads the edited file back: an object of string,
// number or boolean values, ****** standing for the old value of a
// sensitive key.
func parseEditedConfig(text string, old map[string]string) (map[string]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, jsonLineErr(text, err)
	}
	cfg := make(map[string]string, len(raw))
	for _, k := range slices.Sorted(maps.Keys(raw)) {
		var v any
		d := json.NewDecoder(bytes.NewReader(raw[k]))
		d.UseNumber()
		if err := d.Decode(&v); err != nil {
			return nil, err
		}
		switch x := v.(type) {
		case string:
			cfg[k] = x
		case json.Number:
			cfg[k] = x.String()
		case bool:
			cfg[k] = strconv.FormatBool(x)
		default:
			return nil, fmt.Errorf("%s: write the value as a string", k)
		}
		if cfg[k] != maskedValue || !sensitiveConnectKey(k) {
			continue
		}
		o, ok := old[k]
		if !ok {
			return nil, fmt.Errorf("%s: type the value, not %s", k, maskedValue)
		}
		cfg[k] = o
	}
	return cfg, nil
}

// jsonLineErr names the line of a JSON syntax error.
func jsonLineErr(text string, err error) error {
	var se *json.SyntaxError
	off := int64(len(text))
	if errors.As(err, &se) {
		off = se.Offset
	}
	line := 1 + strings.Count(text[:min(int(off), len(text))], "\n")
	return fmt.Errorf("line %d: %w", line, err)
}

func (t *connectorTab) validate(text string) tea.Cmd {
	old := t.editOld
	cfg, err := parseEditedConfig(text, old)
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	if maps.Equal(cfg, old) {
		t.setStatus("no changes", false)
		return nil
	}
	t.setStatus("validating…", false)
	class := cfg["connector.class"]
	id, b, conn, cluster := t.id, t.backend, t.conn, t.cluster
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := connectorValidatedMsg{tabID: id, conn: conn, old: old, cfg: cfg}
		c, err := b.KafkaConnect(ctx, conn, cluster)
		if err == nil {
			msg.problems, err = c.Validate(ctx, class, cfg)
		}
		msg.err = err
		return msg
	}
}

// configChanges are the confirmation's lines: each changed key (a
// sensitive one without its values), added and removed key.
func configChanges(old, cfg map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(cfg)) {
		o, had := old[k]
		switch {
		case !had && sensitiveConnectKey(k):
			out = append(out, "+ "+k)
		case !had:
			out = append(out, "+ "+k+" = "+cfg[k])
		case o == cfg[k]:
		case sensitiveConnectKey(k):
			out = append(out, k+" (changed)")
		default:
			out = append(out, k+" "+o+" → "+cfg[k])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(old)) {
		if _, ok := cfg[k]; !ok {
			out = append(out, "- "+k)
		}
	}
	return out
}

func (t *connectorTab) confirmConfig(m connectorValidatedMsg) tea.Cmd {
	t.setStatus("", false)
	if len(m.problems) > 0 {
		var lines []string
		for _, p := range m.problems {
			lines = append(lines, p.Name+": "+strings.Join(p.Errors, "; "))
		}
		return openOverlay(overlay.NewConfirm("Connector config", lines, "", nil,
			overlay.WithDanger(), overlay.WithBlocked("the cluster rejects this config")))
	}
	lines := append([]string{fmt.Sprintf("Change the config of %s?", t.name)}, configChanges(m.old, m.cfg)...)
	cmd, why := kafkaConfirm(t.backend, t.conn, kmsgx.OpAlterConnectorConfig, "Connector config", lines, t.name,
		connectorSetMsg{tabID: t.id, old: m.old, cfg: m.cfg})
	if why != "" {
		t.setStatus(why, true)
	}
	return cmd
}

// opsUpdate handles the write messages of the tab.
func (t *connectorTab) opsUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case connectorOpMsg:
		t.setStatus("…", false)
		return t.runOp(m), true
	case connectorOpDoneMsg:
		if m.err != nil {
			t.setStatus(kafkaErr(m.err), true)
			return t.load(), true
		}
		t.setStatus(m.status, false)
		cmds := []tea.Cmd{t.load()}
		if t.interval > 0 {
			id := t.id
			cmds = append(cmds, tea.Tick(connectorRecheck, func(time.Time) tea.Msg { return connectorRecheckMsg{tabID: id} }))
		}
		return tea.Batch(cmds...), true
	case connectorRecheckMsg:
		if t.loading {
			return nil, true
		}
		return t.load(), true
	case editFileMsg:
		if m.err != nil {
			t.setStatus("cannot create temp file: "+m.err.Error(), true)
			return nil, true
		}
		t.editPath = m.path
		argv := append(slices.Clone(t.settings.EditorCommand(os.Getenv)), m.path)
		id, path := t.id, m.path
		return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg { //nolint:gosec // the user's own editor
			return editDoneMsg{tabID: id, path: path, err: err}
		}), true
	case editDoneMsg:
		return readEditedFile(t.id, m), true
	case editTextMsg:
		if m.err != nil {
			t.setStatus("editor: "+m.err.Error(), true)
			return nil, true
		}
		return t.validate(m.text), true
	case connectorValidatedMsg:
		if m.err != nil {
			t.setStatus("validate: "+kafkaErr(m.err), true)
			return nil, true
		}
		return t.confirmConfig(m), true
	case connectorSetMsg:
		t.setStatus("saving the config…", false)
		name := t.name
		return t.write("saved the config of "+name, func(ctx context.Context, c kafka.Connect) error {
			return c.SetConfig(ctx, name, m.old, m.cfg)
		}), true
	}
	return nil, false
}
