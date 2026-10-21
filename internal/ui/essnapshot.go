package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

const esSecSnapshots = "snapshots"

// newRepoTemplate is what a new repository's $EDITOR starts with.
const newRepoTemplate = "{\n  \"type\": \"fs\",\n  \"settings\": {\n    \"location\": \"\"\n  }\n}\n"

func snapshotKey(repo, name string) string { return repo + "/" + name }

// parseSnapshotKey splits a snapshot node's Key.
func parseSnapshotKey(key string) (repo, name string) {
	repo, name, _ = strings.Cut(key, "/")
	return repo, name
}

func repoDetail(r es.Repository) string {
	if loc := r.Settings["location"]; r.Type == "fs" && loc != "" {
		return r.Type + " · " + loc
	}
	return r.Type
}

func buildRepoNodes(conn string, repos []es.Repository) []*tree.Node {
	out := make([]*tree.Node, len(repos))
	for i, r := range repos {
		out[i] = &tree.Node{Kind: tree.KindESRepo, Conn: conn, Section: esSecSnapshots, Key: r.Name, Name: r.Name, Detail: repoDetail(r)}
	}
	return out
}

// snapshotSummary is "SUCCESS · 2026-10-22 15:30 · 12s · 8 indices".
func snapshotSummary(s es.Snapshot) string {
	parts := []string{s.State}
	if !s.Start.IsZero() {
		parts = append(parts, s.Start.Local().Format("2006-01-02 15:04"))
	}
	parts = append(parts, s.Duration.Round(time.Millisecond).String(), indexCount(s.Indices))
	return strings.Join(parts, " · ")
}

// loadESSnapshots reads the snapshots of the repository node n, and only
// those: a repository that can't be read fails its own node.
func loadESSnapshots(b Backend, n *tree.Node) tea.Cmd {
	conn, repo := n.Conn, n.Key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := childrenLoadedMsg{node: n}
		var snaps []es.Snapshot
		msg.err = withES(ctx, b, conn, func(d es.Doer) error {
			var err error
			snaps, err = es.Snapshots(ctx, d, repo)
			return err
		})
		if msg.err != nil {
			msg.err = fmt.Errorf("%s", esReason(msg.err))
		}
		for _, s := range snaps {
			msg.children = append(msg.children, &tree.Node{
				Kind: tree.KindESSnapshot, Conn: conn, Section: esSecSnapshots, Key: snapshotKey(repo, s.Name), Name: s.Name,
				Detail: snapshotSummary(s), Warn: s.State != "SUCCESS",
			})
		}
		return msg
	}
}

// reloadRepoNode reads a repository node's snapshots again if it is open.
func (m *Model) reloadRepoNode(conn, repo string) tea.Cmd {
	root := m.kafkaRoot(conn)
	if root == nil {
		return nil
	}
	var node *tree.Node
	var walk func([]*tree.Node)
	walk = func(nodes []*tree.Node) {
		for _, c := range nodes {
			if c.Kind == tree.KindESRepo && c.Key == repo {
				node = c
			}
			walk(c.Children)
		}
	}
	walk(root.Children)
	if node == nil || node.Loading || !node.Loaded {
		return nil
	}
	m.tree.StartLoad(node)
	return loadESSnapshots(m.backend, node)
}

func esSnapshotTabKey(conn, repo, name string) string {
	return "essnapshot\x00" + conn + "\x00" + repo + "\x00" + name
}

// esOpenSnapshotMsg opens a snapshot's tab.
type esOpenSnapshotMsg struct{ conn, repo, name string }

type esSnapshotLoadedMsg struct {
	tabID, gen int
	conn       string
	snap       es.SnapshotDetail
	err        error
}

func (m esSnapshotLoadedMsg) TabID() int               { return m.tabID }
func (m esSnapshotLoadedMsg) esMsg()                   {}
func (m esSnapshotLoadedMsg) failure() (string, error) { return m.conn, m.err }

type esSnapshotTickMsg struct{ tabID, gen int }

func (m esSnapshotTickMsg) TabID() int { return m.tabID }

// esSnapshotTab shows one snapshot (spec §8.1), read again every interval
// while it runs and the tab is visible.
type esSnapshotTab struct {
	id                   int
	backend              Backend
	keys                 *keymap.Keymap
	conn, repo, name     string
	interval             time.Duration
	grid                 *grid.Model
	snap                 es.SnapshotDetail
	loaded, loading      bool
	visible              bool
	gen                  int
	err, status          string
	statusE, tickPending bool
	width, height        int
}

func (m *Model) openESSnapshot(msg esOpenSnapshotMsg) tea.Cmd {
	return m.showTab(esSnapshotTabKey(msg.conn, msg.repo, msg.name), func(id int) tab {
		return &esSnapshotTab{
			id: id, backend: m.backend, keys: m.keys, conn: msg.conn, repo: msg.repo, name: msg.name,
			interval: m.timings.esnapshot, visible: true, grid: grid.New(nil, max(m.settings.MaxColumnWidth, 120)),
		}
	})
}

func (t *esSnapshotTab) ID() int                        { return t.id }
func (t *esSnapshotTab) Title() string                  { return t.name + " · snapshot" }
func (t *esSnapshotTab) Key() string                    { return esSnapshotTabKey(t.conn, t.repo, t.name) }
func (t *esSnapshotTab) Context() keymap.Context        { return keymap.ESSnapshot }
func (t *esSnapshotTab) Target() (string, db.TableRef)  { return t.conn, db.TableRef{} }
func (t *esSnapshotTab) SetSize(w, h int)               { t.width, t.height = max(w, 0), max(h, 0) }
func (t *esSnapshotTab) Init() tea.Cmd                  { return t.load() }
func (t *esSnapshotTab) retryAfterPassword() tea.Cmd    { return t.load() }
func (t *esSnapshotTab) activeGrid() *grid.Model        { return t.grid }
func (t *esSnapshotTab) Find(q string, fwd bool) bool   { return t.grid.Find(q, fwd) }
func (t *esSnapshotTab) setStatus(s string, isErr bool) { t.status, t.statusE = s, isErr }

func (t *esSnapshotTab) exportSource() exportSrc {
	return exportSrc{rs: t.grid.Result(), name: t.conn + "-" + t.repo + "-" + t.name, conn: t.conn}
}

// ticking reports a reread scheduled.
func (t *esSnapshotTab) ticking() bool { return t.tickPending }

// Shown reads the snapshot at once.
func (t *esSnapshotTab) Shown() tea.Cmd {
	t.visible = true
	if t.loading {
		return nil
	}
	return t.load()
}

// Hidden stops the rereads.
func (t *esSnapshotTab) Hidden() {
	t.visible, t.loading, t.tickPending = false, false, false
	t.gen++
}

func (t *esSnapshotTab) load() tea.Cmd {
	t.loading, t.tickPending = true, false
	t.gen++
	id, gen, b, conn, repo, name := t.id, t.gen, t.backend, t.conn, t.repo, t.name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esSnapshotLoadedMsg{tabID: id, gen: gen, conn: conn}
		out.err = withES(ctx, b, conn, func(d es.Doer) error {
			var err error
			out.snap, err = es.GetSnapshot(ctx, d, repo, name)
			return err
		})
		return out
	}
}

func (t *esSnapshotTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case esSnapshotLoadedMsg:
		if m.gen != t.gen {
			return nil
		}
		t.loading = false
		if m.err != nil {
			t.err = esReason(m.err)
			return nil
		}
		t.err, t.loaded, t.snap = "", true, m.snap
		t.grid.ReplaceResult(t.result())
		if t.snap.State != "IN_PROGRESS" || !t.visible {
			return nil
		}
		t.tickPending = true
		if t.interval <= 0 {
			return nil
		}
		id, gen := t.id, t.gen
		return tea.Tick(t.interval, func(time.Time) tea.Msg { return esSnapshotTickMsg{tabID: id, gen: gen} })
	case esSnapshotTickMsg:
		if m.gen == t.gen && t.visible && !t.loading {
			return t.load()
		}
	}
	return nil
}

func snapshotSize(n int64) string {
	if n < 0 {
		return "unknown"
	}
	return humanBytes(n)
}

func (t *esSnapshotTab) result() *db.ResultSet {
	s := t.snap
	when := func(v time.Time) string {
		if v.IsZero() {
			return "—"
		}
		return v.Local().Format("2006-01-02 15:04:05")
	}
	yesNo := map[bool]string{true: "yes", false: "no"}
	rows := [][]string{
		{"repository", t.repo},
		{"state", s.State},
		{"start", when(s.Start)},
		{"end", when(s.End)},
		{"duration", s.Duration.Round(time.Millisecond).String()},
		{"indices", fmt.Sprint(s.Indices)},
		{"shards", fmt.Sprintf("%d ok · %d failed", s.Shards-s.FailedShards, s.FailedShards)},
		{"size", snapshotSize(s.SizeBytes)},
	}
	if s.State == "IN_PROGRESS" && s.SizeBytes > 0 && s.DoneBytes >= 0 {
		rows = append(rows, []string{"progress", fmt.Sprintf("%s / %s (%d%%)", humanBytes(s.DoneBytes), humanBytes(s.SizeBytes), s.DoneBytes*100/s.SizeBytes)})
	}
	rows = append(rows, []string{"global state", yesNo[s.IncludeGlobalState]}, []string{"version", s.Version})
	list := func(label string, items []string) {
		for i, it := range items {
			if i > 0 {
				label = ""
			}
			rows = append(rows, []string{label, it})
		}
	}
	list("index", s.IndexNames)
	list("data stream", s.DataStreams)
	list("failure", s.Failures)
	return textResult([]string{"field", "value"}, rows)
}

func (t *esSnapshotTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	switch a {
	case keymap.Refresh:
		return t.load()
	case keymap.RestoreSnapshot:
		msg := esRestoreAskMsg{conn: t.conn, repo: t.repo, name: t.name}
		return func() tea.Msg { return msg }
	case keymap.DeleteRow:
		msg := esSnapshotAskDeleteMsg{conn: t.conn, repo: t.repo, name: t.name}
		return func() tea.Msg { return msg }
	}
	cmd, _ := gridAction(t.grid, a)
	return cmd
}

func (t *esSnapshotTab) View(focused bool) string {
	var lines []string
	if t.status != "" {
		st := theme.Muted
		if t.statusE {
			st = theme.Error
		}
		lines = append(lines, st.Render(textutil.Sanitize(t.status)))
	}
	switch {
	case t.err != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.err)))
	case !t.loaded:
		lines = append(lines, theme.Muted.Render("loading…"))
	}
	for i, l := range lines {
		lines[i] = textutil.Fit(l, t.width)
	}
	if t.loaded {
		t.grid.SetSize(t.width, max(t.height-len(lines), 0))
		lines = append(lines, t.grid.View(focused))
	}
	return strings.Join(lines, "\n")
}

// esRestoreAskMsg starts restoring a snapshot.
type esRestoreAskMsg struct{ conn, repo, name string }

// esSnapshotAskDeleteMsg is dd on a snapshot's tab.
type esSnapshotAskDeleteMsg struct{ conn, repo, name string }

// esSnapshotWriteMsg is a confirmed snapshot or repository write.
type esSnapshotWriteMsg struct {
	conn       string
	op         string // "create", "delete", "put repo", "delete repo"
	repo, name string
	snap       es.NewSnapshot
	body       json.RawMessage
}

// esSnapshotDoneMsg is a snapshot or repository write's outcome.
type esSnapshotDoneMsg struct {
	esSnapshotWriteMsg
	err error
}

func (m esSnapshotDoneMsg) failure() (string, error) { return m.conn, m.err }

// newSnapshotOn is o on a repository, a snapshot or Snapshots.
func (m *Model) newSnapshotOn(n *tree.Node) tea.Cmd {
	if m.refuseReadOnly(n.Conn) {
		return nil
	}
	switch n.Kind {
	case tree.KindESRepo:
		m.overlay = overlay.NewSnapshotForm(m.keys, n.Key, esx.SnapshotName(time.Now()))
	case tree.KindESSnapshot:
		repo, _ := parseSnapshotKey(n.Key)
		m.overlay = overlay.NewSnapshotForm(m.keys, repo, esx.SnapshotName(time.Now()))
	default:
		return m.askNewRepo(n.Conn)
	}
	m.esFormConn = n.Conn
	return nil
}

func (m *Model) esSnapshotFormed(msg overlay.SnapshotFormMsg) tea.Cmd {
	conn := m.esFormConn
	what := "all indices"
	if len(msg.Indices) > 0 {
		what = strings.Join(msg.Indices, ", ")
	}
	snap := es.NewSnapshot{Repo: msg.Repo, Name: msg.Name, Indices: msg.Indices}
	path := "_snapshot/" + msg.Repo + "/" + msg.Name
	p := esPlan{
		Class: esx.Classify("PUT", path, nil), Count: -1, Method: "PUT", Path: path,
		Lines: []string{"snapshot " + what + " into " + msg.Repo + " as " + msg.Name},
	}
	if len(msg.Indices) > 0 {
		p.Body = []byte(`{"indices":"` + strings.Join(msg.Indices, ",") + `","include_global_state":false}`)
	}
	return m.confirmES(conn, p, esSnapshotWriteMsg{conn: conn, op: "create", repo: msg.Repo, name: msg.Name, snap: snap})
}

// esNewRepoMsg asks for a new repository's name.
type esNewRepoMsg struct{ conn string }

// esRepoNamedMsg is the name of a new repository.
type esRepoNamedMsg struct{ conn, name string }

// esRepoEditFileMsg is a new repository's temp file, made for $EDITOR.
type esRepoEditFileMsg struct {
	conn, name, path string
	err              error
}

// esRepoEditDoneMsg is $EDITOR closed on a new repository's file.
type esRepoEditDoneMsg struct {
	conn, name, path string
	err              error
}

// esRepoEditedMsg is what $EDITOR left in a new repository's file.
type esRepoEditedMsg struct {
	conn, name, text string
	err              error
}

// esRepoEdit is the repository being written in $EDITOR.
type esRepoEdit struct{ conn, name, path string }

func (m *Model) askNewRepo(conn string) tea.Cmd {
	if m.refuseReadOnly(conn) {
		return nil
	}
	m.overlay = overlay.NewPrompt("New snapshot repository", "Repository name", "", func(text string) tea.Msg {
		return esRepoNamedMsg{conn: conn, name: strings.TrimSpace(text)}
	})
	return nil
}

func (m *Model) esRepoNamed(msg esRepoNamedMsg) tea.Cmd {
	if msg.name == "" || strings.ContainsAny(msg.name, " /,*?\"<>|\\") {
		m.setStatus("not a repository name: "+msg.name, true)
		return nil
	}
	return func() tea.Msg {
		f, err := os.CreateTemp("", "datavim-repository-*.json")
		if err != nil {
			return esRepoEditFileMsg{conn: msg.conn, name: msg.name, err: err}
		}
		_, err = f.WriteString(newRepoTemplate)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return esRepoEditFileMsg{conn: msg.conn, name: msg.name, path: f.Name(), err: err}
	}
}

func (m *Model) esRepoEditFile(msg esRepoEditFileMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("cannot create temp file: "+msg.err.Error(), true)
		return nil
	}
	m.esRepoEdit = esRepoEdit{conn: msg.conn, name: msg.name, path: msg.path}
	argv := append(slices.Clone(m.settings.EditorCommand(os.Getenv)), msg.path)
	return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg { //nolint:gosec // the user's own editor
		return esRepoEditDoneMsg{conn: msg.conn, name: msg.name, path: msg.path, err: err}
	})
}

func esRepoEditRead(msg esRepoEditDoneMsg) tea.Cmd {
	return func() tea.Msg {
		defer func() { _ = os.Remove(msg.path) }()
		if msg.err != nil {
			return esRepoEditedMsg{conn: msg.conn, name: msg.name, err: msg.err}
		}
		data, err := os.ReadFile(msg.path)
		return esRepoEditedMsg{conn: msg.conn, name: msg.name, text: string(data), err: err}
	}
}

func (m *Model) esRepoEdited(msg esRepoEditedMsg) tea.Cmd {
	m.esRepoEdit = esRepoEdit{}
	if msg.err != nil {
		m.setStatus("editor: "+msg.err.Error(), true)
		return nil
	}
	var v struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(msg.text), &v); err != nil || v.Type == "" {
		m.setStatus(`not a repository: want {"type": …, "settings": {…}}`, true)
		return nil
	}
	var body bytes.Buffer
	_ = json.Compact(&body, []byte(msg.text))
	path := "_snapshot/" + msg.name
	p := esPlan{
		Class: esx.Classify("PUT", path, body.Bytes()), Count: -1, Method: "PUT", Path: path, Body: []byte(msg.text), Expect: msg.name,
		Lines: []string{"register snapshot repository " + msg.name + " (" + v.Type + ")"},
	}
	return m.confirmES(msg.conn, p, esSnapshotWriteMsg{conn: msg.conn, op: "put repo", repo: msg.name, body: body.Bytes()})
}

// askDeleteSnapshotNode is dd on a repository or snapshot node.
func (m *Model) askDeleteSnapshotNode(n *tree.Node) tea.Cmd {
	if n.Kind == tree.KindESRepo {
		path := "_snapshot/" + n.Key
		p := esPlan{
			Class: esx.Classify("DELETE", path, nil), Count: -1, Method: "DELETE", Path: path, Expect: n.Key,
			Lines: []string{"unregister repository " + n.Key + " (the snapshot files stay in the storage)"},
		}
		return m.confirmES(n.Conn, p, esSnapshotWriteMsg{conn: n.Conn, op: "delete repo", repo: n.Key})
	}
	repo, name := parseSnapshotKey(n.Key)
	return m.askDeleteSnapshot(esSnapshotAskDeleteMsg{conn: n.Conn, repo: repo, name: name})
}

func (m *Model) askDeleteSnapshot(msg esSnapshotAskDeleteMsg) tea.Cmd {
	path := "_snapshot/" + msg.repo + "/" + msg.name
	p := esPlan{
		Class: esx.Classify("DELETE", path, nil), Count: -1, Method: "DELETE", Path: path, Expect: msg.name,
		Lines: []string{"delete snapshot " + msg.name + " from " + msg.repo},
	}
	return m.confirmES(msg.conn, p, esSnapshotWriteMsg{conn: msg.conn, op: "delete", repo: msg.repo, name: msg.name})
}

func (m *Model) esSnapshotWrite(msg esSnapshotWriteMsg) tea.Cmd {
	b := m.backend
	m.setStatus("working…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esSnapshotDoneMsg{esSnapshotWriteMsg: msg}
		out.err = withES(ctx, b, msg.conn, func(d es.Doer) error {
			switch msg.op {
			case "create":
				return es.CreateSnapshot(ctx, d, msg.snap)
			case "delete":
				return es.DeleteSnapshot(ctx, d, msg.repo, msg.name)
			case "put repo":
				return es.PutRepository(ctx, d, msg.repo, msg.body)
			}
			return es.DeleteRepository(ctx, d, msg.repo)
		})
		return out
	}
}

func (m *Model) esSnapshotDone(msg esSnapshotDoneMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(esReason(msg.err), true)
		return nil
	}
	closeWhere := func(match func(key string) bool) []tea.Cmd {
		var cmds []tea.Cmd
		for i := len(m.tabs) - 1; i >= 0; i-- {
			if match(m.tabs[i].Key()) {
				cmds = append(cmds, m.closeTab(i))
			}
		}
		return cmds
	}
	switch msg.op {
	case "create":
		m.setStatus("snapshot "+msg.name+" started in "+msg.repo, false)
		open := esOpenSnapshotMsg{conn: msg.conn, repo: msg.repo, name: msg.name}
		return tea.Batch(m.reloadRepoNode(msg.conn, msg.repo), func() tea.Msg { return open })
	case "delete":
		m.setStatus("deleted snapshot "+msg.name, false)
		key := esSnapshotTabKey(msg.conn, msg.repo, msg.name)
		cmds := closeWhere(func(k string) bool { return k == key })
		return tea.Batch(append(cmds, m.reloadRepoNode(msg.conn, msg.repo))...)
	case "put repo":
		m.setStatus("registered repository "+msg.repo, false)
		return m.reloadESTree(msg.conn)
	}
	m.setStatus("unregistered repository "+msg.repo, false)
	prefix := esSnapshotTabKey(msg.conn, msg.repo, "")
	cmds := closeWhere(func(k string) bool { return strings.HasPrefix(k, prefix) })
	return tea.Batch(append(cmds, m.reloadESTree(msg.conn))...)
}
