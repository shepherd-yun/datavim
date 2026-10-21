package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/kmsgx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// compatLevels are the levels c offers.
var compatLevels = []string{"BACKWARD", "BACKWARD_TRANSITIVE", "FORWARD", "FORWARD_TRANSITIVE", "FULL", "FULL_TRANSITIVE", "NONE"}

type (
	// schemaCheckedMsg is the registry's answer on an edited schema.
	schemaCheckedMsg struct {
		tabID  int
		conn   string
		schema kafka.Schema // what would be registered
		ok     bool
		msgs   []string
		err    error
	}
	// schemaRegisterMsg is the confirmed registration of schema.
	schemaRegisterMsg struct {
		tabID  int
		schema kafka.Schema
	}
	// schemaRegisteredMsg is the outcome of Register.
	schemaRegisteredMsg struct {
		tabID  int
		conn   string
		schema kafka.Schema
		err    error
	}
	// compatPickedMsg is the level chosen with c.
	compatPickedMsg struct {
		tabID int
		level string
	}
	// compatSetMsg is the confirmed SetCompatibility.
	compatSetMsg struct {
		tabID int
		level string
	}
	// compatDoneMsg is the outcome of SetCompatibility.
	compatDoneMsg struct {
		tabID int
		conn  string
		level string
		err   error
	}
	// softDeleteAskMsg asks the root model to confirm a soft delete.
	softDeleteAskMsg struct {
		conn, subject string
		version, id   int
		err           error
	}
	// softDeleteMsg is the confirmed soft delete.
	softDeleteMsg struct {
		conn, subject string
		version       int
	}
	// versionDeletedMsg is the outcome of DeleteVersion.
	versionDeletedMsg struct {
		conn, subject string
		version       int
		err           error
	}
	// kafkaTreeStaleMsg asks the root model to read conn's tree again.
	kafkaTreeStaleMsg struct{ conn string }
)

func (m schemaCheckedMsg) TabID() int                  { return m.tabID }
func (m schemaCheckedMsg) kafkaMsg()                   {}
func (m schemaCheckedMsg) failure() (string, error)    { return m.conn, m.err }
func (m schemaRegisterMsg) TabID() int                 { return m.tabID }
func (m schemaRegisteredMsg) TabID() int               { return m.tabID }
func (m schemaRegisteredMsg) kafkaMsg()                {}
func (m schemaRegisteredMsg) failure() (string, error) { return m.conn, m.err }
func (m compatPickedMsg) TabID() int                   { return m.tabID }
func (m compatSetMsg) TabID() int                      { return m.tabID }
func (m compatDoneMsg) TabID() int                     { return m.tabID }
func (m compatDoneMsg) kafkaMsg()                      {}
func (m compatDoneMsg) failure() (string, error)       { return m.conn, m.err }
func (m softDeleteAskMsg) failure() (string, error)    { return m.conn, m.err }
func (m versionDeletedMsg) failure() (string, error)   { return m.conn, m.err }

// refuseReadOnly says so and reports true on a read-only connection.
func (t *schemaTab) refuseReadOnly() bool {
	if cfg, _ := t.backend.Connection(t.conn); cfg.IsReadOnly() {
		t.setStatus(readOnlyHint, true)
		return true
	}
	return false
}

// handleSchemaOps is the tab's write keys.
func (t *schemaTab) handleSchemaOps(a keymap.Action) (tea.Cmd, bool) {
	switch a {
	case keymap.ExternalEdit:
		return t.editSchema(), true
	case keymap.SetCompatibility:
		return t.compatChoice(), true
	case keymap.DeleteNode:
		if t.section != schVersions || t.refuseReadOnly() {
			return nil, true
		}
		s, ok := t.cursorVersion()
		if !ok {
			return nil, true
		}
		msg := softDeleteAskMsg{conn: t.conn, subject: t.subject, version: s.Version, id: s.ID}
		return func() tea.Msg { return msg }, true
	}
	return nil, false
}

// schemaExt is the temp file's extension for s's type.
func schemaExt(s kafka.Schema) string {
	switch s.Type {
	case "PROTOBUF":
		return ".proto"
	case "JSON":
		return ".json"
	}
	return ".avsc"
}

// editSchema opens the shown version in $EDITOR to register a new one.
func (t *schemaTab) editSchema() tea.Cmd {
	s, ok := t.current()
	if !ok || t.refuseReadOnly() {
		return nil
	}
	return writeTempEdit(t.id, schemaText(s), "datavim-"+safeFileName(t.subject)+"-*"+schemaExt(s))
}

// checkSchema asks the registry whether text may be registered.
func (t *schemaTab) checkSchema(text string) tea.Cmd {
	cur, ok := t.current()
	if !ok {
		return nil
	}
	if strings.TrimSpace(text) == strings.TrimSpace(schemaText(cur)) {
		t.setStatus("no changes", false)
		return nil
	}
	s := kafka.Schema{Type: cur.Type, Text: text, References: cur.References}
	t.setStatus("checking compatibility…", false)
	tabCtx, id, b, conn, subject := t.ctx, t.id, t.backend, t.conn, t.subject
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		msg := schemaCheckedMsg{tabID: id, conn: conn, schema: s}
		reg, err := b.SchemaRegistry(ctx, conn)
		if err == nil {
			msg.ok, msg.msgs, err = reg.Check(ctx, subject, s)
		}
		msg.err = err
		return msg
	}
}

func (t *schemaTab) confirmRegister(m schemaCheckedMsg) tea.Cmd {
	t.setStatus("", false)
	latest := t.latest()
	compat := fmt.Sprintf("v%d (%s)", latest.Version, t.compat)
	if !m.ok {
		why := "NOT compatible"
		if len(m.msgs) > 0 {
			why += ": " + strings.Join(m.msgs, "; ")
		}
		return openOverlay(overlay.NewConfirm("Register schema", []string{why, "with " + compat + "."}, "", nil,
			overlay.WithDanger(0), overlay.WithBlocked("it can't be registered under "+t.compat)))
	}
	lines := []string{"compatible with " + compat, fmt.Sprintf("Register %s v%d?", t.subject, latest.Version+1)}
	cmd, why := kafkaConfirm(t.backend, t.conn, kmsgx.OpRegisterSchema, "Register schema", lines, t.subject,
		schemaRegisterMsg{tabID: t.id, schema: m.schema})
	if why != "" {
		t.setStatus(why, true)
	}
	return cmd
}

func (t *schemaTab) register(s kafka.Schema) tea.Cmd {
	t.setStatus("registering…", false)
	tabCtx, id, b, conn, subject := t.ctx, t.id, t.backend, t.conn, t.subject
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, queryTimeout)
		defer cancel()
		msg := schemaRegisteredMsg{tabID: id, conn: conn}
		reg, err := b.SchemaRegistry(ctx, conn)
		if err == nil {
			msg.schema, err = reg.Register(ctx, subject, s)
		}
		msg.err = err
		return msg
	}
}

// compatChoice is c: the levels, the current one first.
func (t *schemaTab) compatChoice() tea.Cmd {
	if len(t.versions) == 0 || t.refuseReadOnly() {
		return nil
	}
	levels := []string{t.compat}
	for _, l := range compatLevels {
		if l != t.compat {
			levels = append(levels, l)
		}
	}
	id := t.id
	return openOverlay(overlay.NewChoice("Compatibility of "+t.subject, levels, func(i int) tea.Msg {
		return compatPickedMsg{tabID: id, level: levels[i]}
	}))
}

func (t *schemaTab) confirmCompat(level string) tea.Cmd {
	if level == t.compat && !t.global {
		t.setStatus("no changes", false)
		return nil
	}
	cmd, why := kafkaConfirm(t.backend, t.conn, kmsgx.OpSetCompatibility, "Compatibility",
		[]string{fmt.Sprintf("%s: %s → %s", t.subject, t.compat, level)}, t.subject, compatSetMsg{tabID: t.id, level: level})
	if why != "" {
		t.setStatus(why, true)
	}
	return cmd
}

func (t *schemaTab) setCompat(level string) tea.Cmd {
	tabCtx, id, b, conn, subject := t.ctx, t.id, t.backend, t.conn, t.subject
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, queryTimeout)
		defer cancel()
		msg := compatDoneMsg{tabID: id, conn: conn, level: level}
		reg, err := b.SchemaRegistry(ctx, conn)
		if err == nil {
			err = reg.SetCompatibility(ctx, subject, level)
		}
		msg.err = err
		return msg
	}
}

// schemaOpsUpdate handles the write messages of the tab.
func (t *schemaTab) schemaOpsUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
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
		return t.checkSchema(m.text), true
	case schemaCheckedMsg:
		if m.err != nil {
			t.setStatus("check compatibility: "+kafkaErr(m.err), true)
			return nil, true
		}
		return t.confirmRegister(m), true
	case schemaRegisterMsg:
		return t.register(m.schema), true
	case schemaRegisteredMsg:
		if m.err != nil {
			t.setStatus("register: "+kafkaErr(m.err), true)
			return nil, true
		}
		t.version, t.section = m.schema.Version, schSchema
		t.setStatus(fmt.Sprintf("registered %s v%d (id %d)", t.subject, m.schema.Version, m.schema.ID), false)
		conn := t.conn
		return tea.Batch(t.load(), func() tea.Msg { return kafkaTreeStaleMsg{conn: conn} }), true
	case compatPickedMsg:
		return t.confirmCompat(m.level), true
	case compatSetMsg:
		return t.setCompat(m.level), true
	case compatDoneMsg:
		if m.err != nil {
			t.setStatus("set compatibility: "+kafkaErr(m.err), true)
			return nil, true
		}
		t.setStatus(t.subject+" compatibility "+m.level, false)
		return t.load(), true
	}
	return nil, false
}

// askSoftDelete is dd on a version node: its id, then the confirmation.
func (m *Model) askSoftDelete(n *tree.Node) tea.Cmd {
	if m.refuseReadOnly(n.Conn) {
		return nil
	}
	subject, version := subjectVersion(n)
	b, conn := m.backend, n.Conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := softDeleteAskMsg{conn: conn, subject: subject, version: version}
		reg, err := b.SchemaRegistry(ctx, conn)
		var s kafka.Schema
		if err == nil {
			s, err = reg.Schema(ctx, subject, version)
		}
		msg.id, msg.err = s.ID, err
		return msg
	}
}

// softDeleteAsked confirms a soft delete: a dangerous operation, the
// subject's name is typed.
func (m *Model) softDeleteAsked(msg softDeleteAskMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("soft-delete: "+kafkaErr(msg.err), true)
		return nil
	}
	line := fmt.Sprintf("Soft-delete %s v%d? Messages written with it can still be read (id %d stays).", msg.subject, msg.version, msg.id)
	cmd, why := kafkaConfirm(m.backend, msg.conn, kmsgx.OpDeleteSchemaVersion, "Soft-delete version", []string{line}, msg.subject,
		softDeleteMsg{conn: msg.conn, subject: msg.subject, version: msg.version})
	if why != "" {
		m.setStatus(why, true)
	}
	return cmd
}

func (m *Model) softDelete(msg softDeleteMsg) tea.Cmd {
	b := m.backend
	m.setStatus(fmt.Sprintf("soft-deleting %s v%d…", msg.subject, msg.version), false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		out := versionDeletedMsg{conn: msg.conn, subject: msg.subject, version: msg.version}
		reg, err := b.SchemaRegistry(ctx, msg.conn)
		if err == nil {
			err = reg.DeleteVersion(ctx, msg.subject, msg.version)
		}
		out.err = err
		return out
	}
}

// versionDeleted reads the tree and the subject's tab again.
func (m *Model) versionDeleted(msg versionDeletedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(fmt.Sprintf("soft-delete %s v%d: %s", msg.subject, msg.version, kafkaErr(msg.err)), true)
		return nil
	}
	m.setStatus(fmt.Sprintf("soft-deleted %s v%d", msg.subject, msg.version), false)
	cmds := []tea.Cmd{m.reloadKafkaTree(msg.conn)}
	key := schemaTabKey(msg.conn, msg.subject)
	if i := slices.IndexFunc(m.tabs, func(t tab) bool { return t.Key() == key }); i >= 0 {
		st := m.tabs[i].(*schemaTab)
		if st.version == msg.version {
			st.version = -1
		}
		cmds = append(cmds, st.load())
	}
	return tea.Batch(cmds...)
}
