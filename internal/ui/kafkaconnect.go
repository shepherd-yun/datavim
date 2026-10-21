package ui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/session"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// secConnect is the Kafka Connect section of a kafka connection's tree.
const secConnect = "connect"

// sensitiveConnect matches the connector config keys whose values are
// shown as ******.
var sensitiveConnect = regexp.MustCompile(`(?i)(password|secret|token|credentials|jaas|key$)`)

// maskedValue is what a sensitive value shows.
const maskedValue = "******"

// sensitiveConnectKey reports whether a connector config's value is hidden.
func sensitiveConnectKey(k string) bool { return sensitiveConnect.MatchString(k) }

// connectCluster is one Connect cluster of a connection, as the tree last
// read it.
type connectCluster struct {
	name, version, err string
	connectors         []kafka.Connector
}

// kafkaConnectMsg is the reading of every Connect cluster of conn; prompt
// is a password one of them wants.
type kafkaConnectMsg struct {
	conn     string
	gen      int
	clusters []connectCluster
	prompt   error
}

// connectCmd reads every Connect cluster of conn at once, each on its own:
// one that fails or hangs does not hold back the others.
func (m *Model) connectCmd(conn string, st *kafkaTree) tea.Cmd {
	cfg, _ := m.backend.Connection(conn)
	cls, err := config.KafkaConnectClusters(cfg)
	if err != nil || len(cls) == 0 {
		return nil
	}
	b, gen := m.backend, st.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := kafkaConnectMsg{conn: conn, gen: gen, clusters: make([]connectCluster, len(cls))}
		var wg sync.WaitGroup
		var mu sync.Mutex
		for i, cl := range cls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out := connectCluster{name: cl.Name}
				c, err := b.KafkaConnect(ctx, conn, cl.Name)
				if err == nil {
					if out.version, err = c.Version(ctx); err == nil {
						out.connectors, err = c.Connectors(ctx)
					}
				}
				var se *session.SecretRequiredError
				if errors.As(err, &se) {
					mu.Lock()
					msg.prompt = err
					mu.Unlock()
					out.err = "Kafka Connect wants a password"
				} else if err != nil {
					out.err = err.Error()
				}
				msg.clusters[i] = out
			}()
		}
		wg.Wait()
		return msg
	}
}

func (m *Model) kafkaConnectLoaded(msg kafkaConnectMsg) {
	st := m.kafkaTrees[msg.conn]
	if st == nil || msg.gen != st.gen {
		return
	}
	st.connect = msg.clusters
	m.buildKafkaTree(msg.conn)
	if o := m.userPrompt(msg.prompt); o != nil && m.overlay == nil {
		m.overlay = o
		m.retryNode = m.kafkaRoot(msg.conn) // the password reloads the tree
	}
}

// connectorKey is a KindConnector node's Key.
func connectorKey(cluster, name string) string { return cluster + "\x00" + name }

func connectorOf(n *tree.Node) (cluster, name string) {
	cluster, name, _ = strings.Cut(n.Key, "\x00")
	return cluster, name
}

// connectorDetail is "RUNNING 2/3" (running tasks of all), or the state;
// warn is a failed connector or task.
func connectorDetail(c kafka.Connector) (detail string, warn bool) {
	running := 0
	for _, t := range c.Tasks {
		switch t.State {
		case "RUNNING":
			running++
		case "FAILED":
			warn = true
		}
	}
	if c.State == "FAILED" {
		warn = true
	}
	if c.State == "RUNNING" && len(c.Tasks) > 0 {
		return fmt.Sprintf("%s %d/%d", c.State, running, len(c.Tasks)), warn
	}
	return c.State, warn
}

func buildConnectorNodes(conn, cluster string, cs []kafka.Connector) []*tree.Node {
	out := make([]*tree.Node, len(cs))
	for i, c := range cs {
		detail, warn := connectorDetail(c)
		out[i] = &tree.Node{
			Kind: tree.KindConnector, Conn: conn, Section: secConnect, Key: connectorKey(cluster, c.Name),
			Name: c.Name, Detail: detail, Warn: warn,
		}
	}
	return out
}

// connectSection is the Kafka Connect section: the connectors of the one
// cluster, or a node per cluster.
func (m *Model) connectSection(conn string, st *kafkaTree) *tree.Node {
	total := 0
	for _, cl := range st.connect {
		total += len(cl.connectors)
	}
	var children []*tree.Node
	if len(st.connect) == 1 {
		children = buildConnectorNodes(conn, st.connect[0].name, st.connect[0].connectors)
	} else {
		for _, cl := range st.connect {
			n := &tree.Node{Kind: tree.KindConnectCluster, Conn: conn, Section: secConnect, Key: cl.name, Name: cl.name, Err: cl.err}
			if cl.err == "" {
				n.Detail = cl.version + " · " + plural(len(cl.connectors), "connector")
			}
			tree.Adopt(n, buildConnectorNodes(conn, cl.name, cl.connectors))
			children = append(children, n)
		}
	}
	n := m.kafkaSection(conn, secConnect, "Kafka Connect", total, children)
	if len(st.connect) == 1 && st.connect[0].err != "" {
		n.Err = st.connect[0].err
	}
	return n
}

// The parts of a connector tab.
type connectorSection int

const (
	secCStatus connectorSection = iota
	secCTasks
	secCConfig
	connectorSections
)

var connectorSectionNames = [connectorSections]string{"Status", "Tasks", "Config"}

func connectorTabKey(conn, cluster, name string) string {
	return "kconnector\x00" + conn + "\x00" + cluster + "\x00" + name
}

// openConnector opens (or shows) the tab of one connector.
func (m *Model) openConnector(conn, cluster, name string) tea.Cmd {
	return m.showTab(connectorTabKey(conn, cluster, name), func(id int) tab {
		return newConnectorTab(id, m.backend, m.keys, m.settings, conn, cluster, name, m.timings.kserver)
	})
}

type (
	// connectorLoadedMsg is one reading of the connector.
	connectorLoadedMsg struct {
		tabID, gen int
		conn       string
		c          kafka.Connector
		err        error
	}
	connectorTickMsg struct{ tabID, gen int }
)

func (m connectorLoadedMsg) TabID() int               { return m.tabID }
func (m connectorLoadedMsg) kafkaMsg()                {}
func (m connectorLoadedMsg) failure() (string, error) { return m.conn, m.err }
func (m connectorTickMsg) TabID() int                 { return m.tabID }

// connectorTab is one connector: its status, tasks and config, read again
// every interval while on screen.
type connectorTab struct {
	id                  int
	backend             Backend
	keys                *keymap.Keymap
	conn, cluster, name string
	interval            time.Duration
	section             connectorSection
	grids               [connectorSections]*grid.Model
	c                   kafka.Connector
	traces              []string // the Status and Tasks rows' full traces
	loaded, loading     bool
	visible             bool
	gen                 int
	err                 string
	status              string
	statusE             bool

	width, height int
}

func newConnectorTab(id int, b Backend, keys *keymap.Keymap, s config.Settings, conn, cluster, name string, interval time.Duration) *connectorTab {
	t := &connectorTab{id: id, backend: b, keys: keys, conn: conn, cluster: cluster, name: name, interval: interval}
	for i := range t.grids {
		t.grids[i] = grid.New(nil, s.MaxColumnWidth)
	}
	return t
}

func (t *connectorTab) ID() int                       { return t.id }
func (t *connectorTab) Title() string                 { return t.cluster + "/" + t.name }
func (t *connectorTab) Key() string                   { return connectorTabKey(t.conn, t.cluster, t.name) }
func (t *connectorTab) Context() keymap.Context       { return keymap.KafkaConnect }
func (t *connectorTab) Target() (string, db.TableRef) { return t.conn, db.TableRef{} }
func (t *connectorTab) SetSize(w, h int)              { t.width, t.height = max(w, 0), max(h, 0) }
func (t *connectorTab) Init() tea.Cmd                 { return t.load() }
func (t *connectorTab) retryAfterPassword() tea.Cmd   { return t.load() }
func (t *connectorTab) activeGrid() *grid.Model       { return t.grids[t.section] }
func (t *connectorTab) setStatus(s string, isErr bool) {
	t.status, t.statusE = s, isErr
}

func (t *connectorTab) Find(q string, forward bool) bool { return t.activeGrid().Find(q, forward) }

func (t *connectorTab) exportSource() exportSrc {
	name := t.cluster + "-" + t.name + "-" + strings.ToLower(connectorSectionNames[t.section])
	return exportSrc{rs: t.activeGrid().Result(), name: name, conn: t.conn}
}

// Shown reads the connector at once and goes on refreshing.
func (t *connectorTab) Shown() tea.Cmd {
	t.visible = true
	if t.loading {
		return nil
	}
	return t.load()
}

// Hidden stops the refreshes.
func (t *connectorTab) Hidden() {
	t.visible = false
	t.gen++
	t.loading = false
}

func (t *connectorTab) load() tea.Cmd {
	t.loading = true
	t.gen++
	id, gen, b, conn, cluster, name := t.id, t.gen, t.backend, t.conn, t.cluster, t.name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := connectorLoadedMsg{tabID: id, gen: gen, conn: conn}
		c, err := b.KafkaConnect(ctx, conn, cluster)
		if err == nil {
			msg.c, err = c.Connector(ctx, name)
		}
		msg.err = err
		return msg
	}
}

func (t *connectorTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case connectorLoadedMsg:
		if m.gen != t.gen {
			return nil
		}
		t.loading = false
		if m.err != nil {
			t.err = kafkaErr(m.err)
		} else {
			t.err, t.loaded = "", true
			t.apply(m.c)
		}
		if !t.visible || t.interval <= 0 {
			return nil
		}
		id, gen := t.id, t.gen
		return tea.Tick(t.interval, func(time.Time) tea.Msg { return connectorTickMsg{tabID: id, gen: gen} })
	case connectorTickMsg:
		if m.gen == t.gen && t.visible && !t.loading {
			return t.load()
		}
	}
	return nil
}

func (t *connectorTab) apply(c kafka.Connector) {
	t.c = c
	detail, _ := connectorDetail(c)
	status := [][]string{
		{"state", detail},
		{"worker", c.Worker},
		{"type", c.Type},
		{"class", c.Class},
		{"tasks", strconv.Itoa(len(c.Tasks))},
	}
	t.traces = []string{"", "", "", "", ""}
	if c.Trace != "" {
		status = append(status, []string{"trace", strings.TrimSpace(firstLine(c.Trace))})
		t.traces = append(t.traces, c.Trace)
	}
	tasks := make([][]string, len(c.Tasks))
	for i, tk := range c.Tasks {
		tasks[i] = []string{strconv.Itoa(tk.ID), tk.State, tk.Worker, strings.TrimSpace(firstLine(tk.Trace))}
	}
	var cfg [][]string
	for _, k := range slices.Sorted(maps.Keys(c.Config)) {
		v := c.Config[k]
		if sensitiveConnectKey(k) {
			v = maskedValue
		}
		cfg = append(cfg, []string{k, v})
	}
	t.grids[secCStatus].ReplaceResult(textResult([]string{"field", "value"}, status))
	t.grids[secCTasks].ReplaceResult(textResult([]string{"ID", "STATE", "WORKER", "TRACE"}, tasks))
	t.grids[secCConfig].ReplaceResult(textResult([]string{"KEY", "VALUE"}, cfg))
	t.grids[secCTasks].SetMarks(func(row, _ int) grid.Mark {
		if row < len(c.Tasks) && c.Tasks[row].State == "FAILED" {
			return grid.MarkError
		}
		return grid.MarkNone
	})
}

// trace is the full trace of the row under the cursor, if it has one.
func (t *connectorTab) trace() (string, bool) {
	r, _ := t.activeGrid().Position()
	switch t.section {
	case secCStatus:
		if r < len(t.traces) && t.traces[r] != "" {
			return t.traces[r], true
		}
	case secCTasks:
		if r < len(t.c.Tasks) && t.c.Tasks[r].Trace != "" {
			return t.c.Tasks[r].Trace, true
		}
	}
	return "", false
}

func (t *connectorTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	switch a {
	case keymap.NextSection:
		t.section = min(t.section+1, connectorSections-1)
		return nil
	case keymap.PrevSection:
		t.section = max(t.section-1, 0)
		return nil
	case keymap.Refresh:
		if !t.loading {
			return t.load()
		}
		return nil
	case keymap.CellDetail:
		if tr, ok := t.trace(); ok {
			return openOverlay(overlay.NewDetail("trace", tr))
		}
	}
	if cmd, ok := t.handleOps(a); ok {
		return cmd
	}
	cmd, _ := gridAction(t.activeGrid(), a)
	return cmd
}

func (t *connectorTab) View(focused bool) string {
	names := make([]string, connectorSections)
	for i, n := range connectorSectionNames {
		if connectorSection(i) == t.section {
			names[i] = theme.Title.Render(n)
		} else {
			names[i] = theme.Muted.Render(n)
		}
	}
	head := strings.Join(names, theme.Muted.Render(" · "))
	if t.status != "" {
		style := theme.Muted
		if t.statusE {
			style = theme.Error
		}
		head += theme.Muted.Render("  ") + style.Render(textutil.Sanitize(t.status))
	}
	lines := []string{head}
	var g *grid.Model
	switch {
	case t.err != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.err)))
	case !t.loaded:
		lines = append(lines, theme.Muted.Render("loading…"))
	default:
		g = t.activeGrid()
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
