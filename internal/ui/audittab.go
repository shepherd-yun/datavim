package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// auditPage is how many audit entries one read returns.
const auditPage = 1000

var auditColumns = []string{"TIME", "CONN", "KIND", "DB", "OP", "OBJECT", "DETAIL", "RESULT", "TOOK"}

// auditTabKey is the key of the audit tab of conn ("" = every connection).
func auditTabKey(conn string) string { return "audit\x00" + conn }

// openAudit opens (or shows) the audit log of conn, or of every connection.
func (m *Model) openAudit(conn string) tea.Cmd {
	if conn != "" {
		if _, ok := m.backend.Connection(conn); !ok {
			m.setStatus(fmt.Sprintf("unknown connection %q", conn), true)
			return nil
		}
	}
	return m.showTab(auditTabKey(conn), func(id int) tab {
		return newAuditTab(id, m.store, m.keys, m.settings, conn)
	})
}

// auditLoadedMsg is one page of the audit log; more appends to the rows.
type auditLoadedMsg struct {
	tabID, gen int
	entries    []history.AuditEntry
	more       bool
	err        error
}

func (m auditLoadedMsg) TabID() int { return m.tabID }

type auditFilterMsg struct {
	tabID int
	text  string
}

func (m auditFilterMsg) TabID() int { return m.tabID }

// auditGotoMsg asks the root model to open the object of an entry.
type auditGotoMsg struct{ e history.AuditEntry }

// auditTab lists the writes sqlvim sent, newest first; F searches them.
type auditTab struct {
	id      int
	store   Store
	keys    *keymap.Keymap
	conn    string
	text    string // F's search
	grid    *grid.Model
	entries []history.AuditEntry
	done    bool // no older page
	loading bool
	gen     int
	err     string

	width, height int
}

func newAuditTab(id int, st Store, keys *keymap.Keymap, s config.Settings, conn string) *auditTab {
	return &auditTab{id: id, store: st, keys: keys, conn: conn, grid: grid.New(nil, s.MaxColumnWidth)}
}

func (t *auditTab) ID() int { return t.id }
func (t *auditTab) Title() string {
	if t.conn == "" {
		return "audit"
	}
	return "audit · " + t.conn
}
func (t *auditTab) Key() string                      { return auditTabKey(t.conn) }
func (t *auditTab) Context() keymap.Context          { return keymap.Audit }
func (t *auditTab) Target() (string, db.TableRef)    { return t.conn, db.TableRef{} }
func (t *auditTab) SetSize(w, h int)                 { t.width, t.height = max(w, 0), max(h, 0) }
func (t *auditTab) Init() tea.Cmd                    { return t.load(false) }
func (t *auditTab) Find(q string, forward bool) bool { return t.grid.Find(q, forward) }

func (t *auditTab) exportSource() exportSrc {
	return exportSrc{rs: t.grid.Result(), name: "audit", conn: t.conn}
}

// load reads the first page, or the page after the last row when more.
func (t *auditTab) load(more bool) tea.Cmd {
	if t.store == nil {
		return nil
	}
	t.loading = true
	if !more {
		t.gen++
	}
	q := history.AuditQuery{Conn: t.conn, Text: t.text, Limit: auditPage}
	if more && len(t.entries) > 0 {
		q.BeforeID = t.entries[len(t.entries)-1].ID
	}
	id, gen, st := t.id, t.gen, t.store
	return func() tea.Msg {
		es, err := st.Audit(q)
		return auditLoadedMsg{tabID: id, gen: gen, entries: es, more: more, err: err}
	}
}

func (t *auditTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case auditLoadedMsg:
		if m.gen != t.gen {
			return nil
		}
		t.loading = false
		if m.err != nil {
			t.err = m.err.Error()
			return nil
		}
		t.err = ""
		row, col := t.grid.Position()
		if m.more {
			t.entries = append(t.entries, m.entries...)
		} else {
			t.entries, row = m.entries, 0
		}
		t.done = len(m.entries) < auditPage
		t.render()
		t.grid.SetCursor(row, col)
	case auditFilterMsg:
		t.text = strings.TrimSpace(m.text)
		return t.load(false)
	}
	return nil
}

func (t *auditTab) render() {
	rows := make([][]string, len(t.entries))
	for i, e := range t.entries {
		result := "ok"
		if e.Err != "" {
			result, _, _ = strings.Cut(e.Err, "\n")
		}
		rows[i] = []string{
			e.Time.Local().Format("2006-01-02 15:04:05"), e.Conn, e.Kind, e.Database, e.Op, e.Object,
			strings.Join(strings.Fields(e.Detail), " "), result, e.Duration.String(),
		}
	}
	t.grid.ReplaceResult(textResult(auditColumns, rows))
	t.grid.SetMarks(func(row, _ int) grid.Mark {
		if row < len(t.entries) && t.entries[row].Err != "" {
			return grid.MarkError
		}
		return grid.MarkNone
	})
}

func (t *auditTab) current() (history.AuditEntry, bool) {
	r, _ := t.grid.Position()
	if r < 0 || r >= len(t.entries) {
		return history.AuditEntry{}, false
	}
	return t.entries[r], true
}

func (t *auditTab) Handle(a keymap.Action) tea.Cmd {
	switch a {
	case keymap.Refresh:
		return t.load(false)
	case keymap.FilterPrompt:
		id := t.id
		return openOverlay(overlay.NewPrompt("Search the audit log", "text in the op, object or detail", t.text,
			func(text string) tea.Msg { return auditFilterMsg{tabID: id, text: text} }))
	case keymap.FilterClear:
		if t.text == "" {
			return nil
		}
		t.text = ""
		return t.load(false)
	case keymap.OpenCellKey:
		if e, ok := t.current(); ok {
			return func() tea.Msg { return auditGotoMsg{e: e} }
		}
		return nil
	case keymap.CellDetail:
		if e, ok := t.current(); ok {
			return openOverlay(overlay.NewDetail(e.Op+" "+e.Object, auditText(e)))
		}
		return nil
	case keymap.Down, keymap.HalfPageDown, keymap.Bottom:
		// at the last row already (or sent there): read the older page
		r, _ := t.grid.Position()
		last := r >= t.grid.RowCount()-1
		cmd, _ := gridAction(t.grid, a)
		if r, _ = t.grid.Position(); (last || a == keymap.Bottom) && r >= t.grid.RowCount()-1 && !t.done && !t.loading {
			return t.load(true)
		}
		return cmd
	}
	cmd, _ := gridAction(t.grid, a)
	return cmd
}

// auditText is the whole entry, for K.
func auditText(e history.AuditEntry) string {
	lines := []string{
		"time:     " + e.Time.Local().Format("2006-01-02 15:04:05.000"),
		"conn:     " + e.Conn,
		"kind:     " + e.Kind,
	}
	if e.Database != "" {
		lines = append(lines, "db:       "+e.Database)
	}
	lines = append(lines, "op:       "+e.Op, "object:   "+e.Object, "took:     "+e.Duration.String())
	if e.Err != "" {
		lines = append(lines, "error:    "+e.Err)
	} else {
		lines = append(lines, "result:   ok")
	}
	if e.Detail != "" {
		lines = append(lines, "", e.Detail)
	}
	return strings.Join(lines, "\n")
}

func (t *auditTab) View(focused bool) string {
	head := theme.Muted.Render(fmt.Sprintf("%s writes", groupDigits(int64(len(t.entries)))))
	if !t.done {
		head = theme.Muted.Render(fmt.Sprintf("the latest %s writes", groupDigits(int64(len(t.entries)))))
	}
	if t.text != "" {
		head += theme.Muted.Render(" · matching ") + strconv.Quote(t.text)
	}
	lines := []string{head}
	var g *grid.Model
	switch {
	case t.err != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.err)))
	case len(t.entries) == 0 && !t.loading:
		lines = append(lines, theme.Muted.Render("no writes recorded yet"))
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

// auditGoto opens what e wrote to, when sqlvim has a tab for it.
func (m *Model) auditGoto(e history.AuditEntry) tea.Cmd {
	if _, ok := m.backend.Connection(e.Conn); !ok {
		m.setStatus(fmt.Sprintf("unknown connection %q", e.Conn), true)
		return nil
	}
	nothing := func() tea.Cmd {
		m.setStatus("nothing to open for "+e.Op, true)
		return nil
	}
	if e.Object == "" {
		return nothing()
	}
	switch e.Kind {
	case history.AuditSQL:
		ref := db.TableRef{Namespace: db.Namespace{Database: e.Database}, Name: e.Object}
		if schema, name, ok := strings.Cut(e.Object, "."); ok {
			ref.Schema, ref.Name = schema, name
		}
		return m.openData(e.Conn, ref)
	case history.AuditRedis:
		n, err := strconv.Atoi(e.Database)
		if err != nil || strings.Contains(e.Op, " ") || e.Op == "FLUSHDB" || e.Op == "FLUSHALL" {
			return nothing()
		}
		return m.openKey(e.Conn, n, e.Object)
	case history.AuditKafka:
		return m.auditGotoKafka(e, nothing)
	}
	return nothing()
}

func (m *Model) auditGotoKafka(e history.AuditEntry, nothing func() tea.Cmd) tea.Cmd {
	switch e.Op {
	case "produce", "create_topic", "delete_topic", "alter_configs", "add_partitions", "delete_records":
		return m.openTopic(e.Conn, e.Object)
	case "reset_offsets", "delete_group", "delete_offsets":
		return m.openGroup(e.Conn, e.Object)
	case "register_schema", "set_compatibility", "delete_schema_version":
		return m.openSchema(e.Conn, e.Object, -1)
	case "create_acl", "delete_acl":
		return m.openACLs(e.Conn)
	case "pause_connector", "resume_connector", "restart_connector", "restart_task", "alter_connector_config":
		if cluster, name, ok := strings.Cut(e.Object, "/"); ok {
			return m.openConnector(e.Conn, cluster, name)
		}
	}
	return nothing()
}
