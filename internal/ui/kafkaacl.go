package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/kmsgx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// secACLs is the tree's ACLs line under a kafka connection: a leaf that
// opens the ACL tab.
const secACLs = "acls"

var aclColumns = []string{"PRINCIPAL", "HOST", "RESOURCE", "NAME", "PATTERN", "OPERATION", "PERMISSION"}

func aclTabKey(conn string) string { return "kacl\x00" + conn }

// openACLs opens (or shows) the ACL tab of conn.
func (m *Model) openACLs(conn string) tea.Cmd {
	return m.showTab(aclTabKey(conn), func(id int) tab {
		return newACLTab(id, m.backend, m.keys, m.settings, conn)
	})
}

type (
	// aclsLoadedMsg is a reading of the ACLs; sel is the ACL to put the
	// cursor on.
	aclsLoadedMsg struct {
		tabID int
		conn  string
		acls  []kafka.ACL
		sel   *kafka.ACL
		err   error
	}
	// aclFilterMsg is the text F filters the ACLs by.
	aclFilterMsg struct {
		tabID int
		text  string
	}
	// aclNewMsg asks the root model for the new-ACL form.
	aclNewMsg struct{ tabID int }
	// aclCreateMsg and aclDeleteMsg are confirmed writes.
	aclCreateMsg struct {
		tabID int
		acls  []kafka.ACL
	}
	aclDeleteMsg struct {
		tabID int
		acl   kafka.ACL
	}
	// aclWrittenMsg is the outcome of a write; status says what it did.
	aclWrittenMsg struct {
		tabID  int
		conn   string
		status string
		sel    *kafka.ACL
		err    error
	}
)

func (m aclsLoadedMsg) TabID() int               { return m.tabID }
func (m aclsLoadedMsg) kafkaMsg()                {}
func (m aclsLoadedMsg) failure() (string, error) { return m.conn, m.err }
func (m aclCreateMsg) TabID() int                { return m.tabID }
func (m aclFilterMsg) TabID() int                { return m.tabID }
func (m aclDeleteMsg) TabID() int                { return m.tabID }
func (m aclWrittenMsg) TabID() int               { return m.tabID }

// aclTab lists a cluster's ACLs; o adds some, dd deletes one.
type aclTab struct {
	id      int
	backend Backend
	keys    *keymap.Keymap
	conn    string
	grid    *grid.Model
	acls    []kafka.ACL
	filter  string      // F: text in the principal, resource type or name
	shown   []kafka.ACL // the acls that pass the filter, one per grid row
	loaded  bool
	err     string
	status  string
	statusE bool

	width, height int
}

func newACLTab(id int, b Backend, keys *keymap.Keymap, s config.Settings, conn string) *aclTab {
	return &aclTab{id: id, backend: b, keys: keys, conn: conn, grid: grid.New(nil, s.MaxColumnWidth)}
}

func (t *aclTab) ID() int                          { return t.id }
func (t *aclTab) Title() string                    { return t.conn + " · ACLs" }
func (t *aclTab) Key() string                      { return aclTabKey(t.conn) }
func (t *aclTab) Context() keymap.Context          { return keymap.KafkaACL }
func (t *aclTab) Target() (string, db.TableRef)    { return t.conn, db.TableRef{} }
func (t *aclTab) SetSize(w, h int)                 { t.width, t.height = max(w, 0), max(h, 0) }
func (t *aclTab) Init() tea.Cmd                    { return t.load(nil) }
func (t *aclTab) retryAfterPassword() tea.Cmd      { return t.load(nil) }
func (t *aclTab) Find(q string, forward bool) bool { return t.grid.Find(q, forward) }
func (t *aclTab) setStatus(s string, isErr bool)   { t.status, t.statusE = s, isErr }

func (t *aclTab) exportSource() exportSrc {
	return exportSrc{rs: t.grid.Result(), name: t.conn + "-acls", conn: t.conn}
}

func (t *aclTab) load(sel *kafka.ACL) tea.Cmd {
	id, b, conn := t.id, t.backend, t.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := aclsLoadedMsg{tabID: id, conn: conn, sel: sel}
		msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
			var err error
			msg.acls, err = c.ACLs(ctx)
			return err
		})
		return msg
	}
}

func (t *aclTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case aclsLoadedMsg:
		t.loaded = true
		if m.err != nil {
			t.err = kafkaErr(m.err)
			return nil
		}
		t.err, t.acls = "", m.acls
		t.render()
		if m.sel != nil {
			if i := slices.Index(t.shown, *m.sel); i >= 0 {
				t.grid.SetCursor(i, 0)
			}
		}
	case aclFilterMsg:
		t.filter = strings.TrimSpace(m.text)
		t.render()
		t.grid.SetCursor(0, 0)
	case aclCreateMsg:
		t.setStatus(fmt.Sprintf("creating %s…", plural(len(m.acls), "ACL")), false)
		return t.write(fmt.Sprintf("created %s", plural(len(m.acls), "ACL")), &m.acls[0], func(ctx context.Context, c kafka.Client) error {
			return c.CreateACLs(ctx, m.acls)
		})
	case aclDeleteMsg:
		t.setStatus("deleting the ACL…", false)
		return t.write("deleted "+m.acl.String(), nil, func(ctx context.Context, c kafka.Client) error {
			_, err := c.DeleteACLs(ctx, []kafka.ACL{m.acl})
			return err
		})
	case aclWrittenMsg:
		if m.err != nil {
			t.setStatus(kafkaErr(m.err), true)
		} else {
			t.setStatus(m.status, false)
		}
		return t.load(m.sel) // a mismatch deleted more: show what is left
	}
	return nil
}

// write runs a confirmed ACL write; a lost connection is not retried.
func (t *aclTab) write(done string, sel *kafka.ACL, fn func(context.Context, kafka.Client) error) tea.Cmd {
	id, b, conn := t.id, t.backend, t.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := aclWrittenMsg{tabID: id, conn: conn, status: done, sel: sel}
		c, err := b.Kafka(ctx, conn)
		if err == nil {
			err = fn(ctx, c)
		}
		msg.err = err
		return msg
	}
}

// aclMatches reports whether a's principal, resource type or name
// contains filter, ignoring case.
func aclMatches(a kafka.ACL, filter string) bool {
	f := strings.ToLower(filter)
	for _, s := range []string{a.Principal, a.ResourceType, a.ResourceName} {
		if strings.Contains(strings.ToLower(s), f) {
			return true
		}
	}
	return false
}

func (t *aclTab) render() {
	t.shown = t.acls
	if t.filter != "" {
		t.shown = slices.DeleteFunc(slices.Clone(t.acls), func(a kafka.ACL) bool { return !aclMatches(a, t.filter) })
	}
	rows := make([][]string, len(t.shown))
	for i, a := range t.shown {
		rows[i] = []string{a.Principal, a.Host, a.ResourceType, a.ResourceName, a.Pattern, a.Operation, a.Permission}
	}
	t.grid.ReplaceResult(textResult(aclColumns, rows))
	t.grid.SetMarks(func(row, _ int) grid.Mark {
		if row < len(t.shown) && t.shown[row].Permission == "deny" {
			return grid.MarkError
		}
		return grid.MarkNone
	})
}

func (t *aclTab) current() (kafka.ACL, bool) {
	r, _ := t.grid.Position()
	if r < 0 || r >= len(t.shown) {
		return kafka.ACL{}, false
	}
	return t.shown[r], true
}

func (t *aclTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	switch a {
	case keymap.Refresh:
		return t.load(nil)
	case keymap.FilterPrompt:
		id := t.id
		return openOverlay(overlay.NewPrompt("Filter ACLs", "text in the principal, resource type or name", t.filter,
			func(text string) tea.Msg { return aclFilterMsg{tabID: id, text: text} }))
	case keymap.FilterClear:
		t.filter = ""
		t.render()
		return nil
	case keymap.NewKey:
		if cfg, _ := t.backend.Connection(t.conn); cfg.IsReadOnly() {
			t.setStatus(readOnlyHint, true)
			return nil
		}
		id := t.id
		return func() tea.Msg { return aclNewMsg{tabID: id} }
	case keymap.DeleteRow:
		acl, ok := t.current()
		if !ok {
			return nil
		}
		cmd, why := kafkaConfirm(t.backend, t.conn, kmsgx.OpDeleteACL, "Delete ACL",
			[]string{"Delete this ACL? " + acl.String()}, acl.Principal, aclDeleteMsg{tabID: t.id, acl: acl})
		if why != "" {
			t.setStatus(why, true)
		}
		return cmd
	case keymap.OpenCellKey:
		acl, ok := t.current()
		if !ok {
			return nil
		}
		if acl.Pattern != "literal" || acl.ResourceName == "*" {
			t.setStatus("nothing to open for a "+acl.Pattern+" or wildcard ACL", true)
			return nil
		}
		conn := t.conn
		switch acl.ResourceType {
		case "topic":
			return func() tea.Msg { return openTopicSectionMsg{conn: conn, topic: acl.ResourceName, section: secMessages} }
		case "group":
			return openGroupCmd(conn, acl.ResourceName)
		}
		t.setStatus("nothing to open for a "+acl.ResourceType+" ACL", true)
		return nil
	}
	cmd, _ := gridAction(t.grid, a)
	return cmd
}

func (t *aclTab) View(focused bool) string {
	head := theme.Muted.Render(plural(len(t.acls), "ACL"))
	if t.filter != "" {
		head += theme.Muted.Render(fmt.Sprintf(" · filter %s · %d shown", t.filter, len(t.shown)))
	}
	if t.status != "" {
		style := theme.Muted
		if t.statusE {
			style = theme.Error
		}
		head += theme.Muted.Render(" · ") + style.Render(textutil.Sanitize(t.status))
	}
	lines := []string{head}
	var g *grid.Model
	switch {
	case t.err != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.err)))
	case !t.loaded:
		lines = append(lines, theme.Muted.Render("loading…"))
	case len(t.acls) == 0:
		lines = append(lines, theme.Muted.Render("no ACLs · "+keyHint(t.keys, keymap.KafkaACL, keymap.NewKey, "adds one")))
	default:
		g = t.grid
	}
	for i, l := range lines {
		lines[i] = textutil.Fit(l, t.width)
	}
	if g != nil {
		g.SetSize(t.width, max(t.height-len(lines), 0))
		lines = append(lines, g.View(focused))
	}
	return strings.Join(lines, "\n")
}

// aclFormFor opens the new-ACL form for the ACL tab id.
func (m *Model) aclFormFor(msg aclNewMsg) {
	m.aclFormTab = msg.tabID
	m.overlay = overlay.NewACLForm(m.keys)
}

// aclFormed confirms the ACLs the form describes (design §8.2: y).
func (m *Model) aclFormed(msg overlay.ACLFormMsg) tea.Cmd {
	t, ok := m.tabByID(m.aclFormTab).(*aclTab)
	if !ok || len(msg.ACLs) == 0 {
		return nil
	}
	lines := make([]string, len(msg.ACLs))
	for i, a := range msg.ACLs {
		lines[i] = a.String()
	}
	cmd, why := kafkaConfirm(m.backend, t.conn, kmsgx.OpCreateACL, fmt.Sprintf("Create %s?", plural(len(msg.ACLs), "ACL")),
		lines, "", aclCreateMsg{tabID: t.id, acls: msg.ACLs})
	if why != "" {
		t.setStatus(why, true)
	}
	return cmd
}
