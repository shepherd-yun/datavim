package ui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/session"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// The sections under an elasticsearch connection.
const (
	esSecIndices   = "indices"
	esSecAliases   = "aliases"
	esSecStreams   = "streams"
	esSecTemplates = "templates"
	esSecPolicies  = "policies"
	esSecPipelines = "pipelines"
)

var errESUnsupported = errors.New("not supported for elasticsearch")

// esSort is the order of the Indices section.
type esSort int

const (
	esByName esSort = iota
	esByDocs
	esBySize
)

// esTree is what the tree of an elasticsearch connection is built from,
// and the view settings that survive a refresh.
type esTree struct {
	info       es.Info
	health     es.Health
	indices    []es.IndexInfo
	aliases    []es.Alias
	streams    []es.DataStream
	templates  []es.Template
	policies   []es.Policy // none on OpenSearch
	pipelines  []es.Pipeline
	repos      []es.Repository
	errs       map[string]string // section -> why it could not load
	showSystem bool
	sortBy     esSort
	filter     string // the Indices section's name filter
}

// targetKind is what an Index tab was opened on.
type targetKind int

const (
	targetIndex targetKind = iota
	targetAlias
	targetPattern
	targetStream
)

// openIndexMsg opens the Index tab of target.
type openIndexMsg struct {
	conn, target string
	kind         targetKind
}

// isES reports an elasticsearch connection.
func isES(b Backend, conn string) bool {
	cfg, ok := b.Connection(conn)
	return ok && cfg.Driver == "elasticsearch"
}

// withES runs fn on conn's Doer; a lost connection is reset and fn
// retried once, as withConn does.
func withES(ctx context.Context, b Backend, conn string, fn func(es.Doer) error) error {
	d, err := b.ES(ctx, conn)
	if err == nil {
		err = fn(d)
	}
	if !db.IsConnLost(err) {
		return err
	}
	slog.Info("elasticsearch connection lost, reconnecting", "conn", conn, "err", err)
	b.Reset(conn)
	if d, err = b.ES(ctx, conn); err != nil {
		return err
	}
	return fn(d)
}

// esReason is how a failed ES call shows: denied and unsupported APIs
// get one fixed line (spec §3.2).
func esReason(err error) string {
	switch {
	case errors.Is(err, es.ErrForbidden):
		return "permission denied"
	case errors.Is(err, es.ErrUnsupported):
		return "not supported by this cluster"
	}
	return err.Error()
}

// esSectionErr keeps a denied or unsupported section's error for its
// node; anything else fails the load.
func esSectionErr(errs map[string]string, sec string, err error) error {
	if errors.Is(err, es.ErrForbidden) || errors.Is(err, es.ErrUnsupported) {
		errs[sec] = esReason(err)
		return nil
	}
	return err
}

// esTreeMsg is the load of an elasticsearch connection's tree.
type esTreeMsg struct {
	node      *tree.Node
	info      es.Info
	health    es.Health
	indices   []es.IndexInfo
	aliases   []es.Alias
	streams   []es.DataStream
	templates []es.Template
	policies  []es.Policy
	pipelines []es.Pipeline
	repos     []es.Repository
	errs      map[string]string
	err       error
}

func (m esTreeMsg) failure() (string, error) { return m.node.Conn, m.err }

// loadESTree reads the indices, aliases and data streams of n's connection.
func loadESTree(b Backend, n *tree.Node) tea.Cmd {
	conn := n.Conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := esTreeMsg{node: n, errs: map[string]string{}}
		msg.err = withES(ctx, b, conn, func(d es.Doer) error {
			var err error
			if msg.health, err = es.ClusterHealth(ctx, d); esSectionErr(msg.errs, "health", err) != nil {
				return err
			}
			if msg.indices, err = es.Indices(ctx, d); esSectionErr(msg.errs, esSecIndices, err) != nil {
				return err
			}
			if msg.aliases, err = es.Aliases(ctx, d); esSectionErr(msg.errs, esSecAliases, err) != nil {
				return err
			}
			if msg.streams, err = es.DataStreams(ctx, d); esSectionErr(msg.errs, esSecStreams, err) != nil {
				return err
			}
			if msg.templates, err = es.Templates(ctx, d); esSectionErr(msg.errs, esSecTemplates, err) != nil {
				return err
			}
			if info, _ := b.ESInfo(conn); !info.OpenSearch() {
				if msg.policies, err = es.Policies(ctx, d); esSectionErr(msg.errs, esSecPolicies, err) != nil {
					return err
				}
			}
			if msg.pipelines, err = es.Pipelines(ctx, d); esSectionErr(msg.errs, esSecPipelines, err) != nil {
				return err
			}
			// no repositories, or none we may see: no Snapshots node
			if msg.repos, err = es.Repositories(ctx, d); err != nil && !errors.Is(err, es.ErrForbidden) && !errors.Is(err, es.ErrUnsupported) {
				msg.errs[esSecSnapshots] = esReason(err)
			}
			return nil
		})
		msg.info, _ = b.ESInfo(conn)
		if msg.err != nil {
			slog.Warn("load elasticsearch tree", "conn", conn, "err", msg.err)
		}
		return msg
	}
}

func (m *Model) esTreeLoaded(msg esTreeMsg) tea.Cmd {
	n := msg.node
	if errors.Is(msg.err, session.ErrPasswordRequired) {
		m.tree.CancelLoad(n)
		m.retryNode = n
		m.overlay = m.passwordPrompt(n.Conn)
		return nil
	}
	if m.userPrompt(msg.err) != nil {
		m.tree.CancelLoad(n)
		m.retryNode = n
		return nil
	}
	if msg.err != nil {
		m.tree.SetChildren(n, nil, msg.err)
		m.setStatus(msg.err.Error(), true)
		return nil
	}
	st := m.esTrees[n.Conn]
	if st == nil {
		cfg, _ := m.backend.Connection(n.Conn)
		st = &esTree{showSystem: cfg.Params["show_system"] == "true"}
		m.esTrees[n.Conn] = st
	}
	st.info, st.health, st.indices, st.aliases, st.streams, st.errs = msg.info, msg.health, msg.indices, msg.aliases, msg.streams, msg.errs
	st.templates, st.policies, st.pipelines, st.repos = msg.templates, msg.policies, msg.pipelines, msg.repos
	m.buildESTree(n.Conn)
	return nil
}

// reloadESTree reads conn's tree again once it has been read.
func (m *Model) reloadESTree(conn string) tea.Cmd {
	root := m.kafkaRoot(conn)
	if root == nil || root.Loading || !root.Loaded {
		return nil
	}
	m.tree.StartLoad(root)
	return loadESTree(m.backend, root)
}

func (m *Model) buildESTree(conn string) {
	root, st := m.kafkaRoot(conn), m.esTrees[conn]
	if root == nil || st == nil {
		return
	}
	open := map[nodeRef]bool{}
	var walk func([]*tree.Node)
	walk = func(nodes []*tree.Node) {
		for _, c := range nodes {
			if c.Expanded {
				open[refOf(c)] = true
			}
			walk(c.Children)
		}
	}
	walk(root.Children)
	var want *nodeRef
	if sel := m.tree.Selected(); sel != nil && sel.Conn == conn && sel != root {
		r := refOf(sel)
		want = &r
	}
	root.Detail = "[" + st.info.Version + " · " + st.health.Status + "]"
	root.Warn = st.health.Status != "" && st.health.Status != "green"

	indices := buildIndexNodes(conn, st)
	sec := func(key, title string, count int, children []*tree.Node) *tree.Node {
		n := &tree.Node{
			Kind: tree.KindESSection, Conn: conn, Section: key, Key: key,
			Name: fmt.Sprintf("%s (%s)", title, groupDigits(int64(count))), Err: st.errs[key],
		}
		tree.Adopt(n, children)
		return n
	}
	idx := sec(esSecIndices, "Indices", countIndexNodes(indices), indices)
	if st.filter != "" {
		idx.Detail = "filter " + st.filter
	}
	sections := []*tree.Node{idx, sec(esSecAliases, "Aliases", countAliases(st.aliases), buildAliasNodes(conn, st.aliases))}
	if len(st.streams) > 0 {
		sections = append(sections, sec(esSecStreams, "Data Streams", len(st.streams), buildStreamNodes(conn, st.streams)))
	}
	templates := buildTemplateNodes(conn, st)
	sections = append(sections, sec(esSecTemplates, "Index Templates", len(templates), templates))
	if !st.info.OpenSearch() {
		policies := buildPolicyNodes(conn, st)
		sections = append(sections, sec(esSecPolicies, "ILM Policies", len(policies), policies))
	}
	pipelines := buildPipelineNodes(conn, st)
	sections = append(sections, sec(esSecPipelines, "Ingest Pipelines", len(pipelines), pipelines))
	if len(st.repos) > 0 || st.errs[esSecSnapshots] != "" {
		sections = append(sections, sec(esSecSnapshots, "Snapshots", len(st.repos), buildRepoNodes(conn, st.repos)))
	}
	var reopen func([]*tree.Node)
	reopen = func(nodes []*tree.Node) {
		for _, c := range nodes {
			if open[refOf(c)] && c.Kind != tree.KindESRepo { // its snapshots load again when expanded
				c.Expanded = true
			}
			reopen(c.Children)
		}
	}
	reopen(sections)
	m.tree.SetChildren(root, sections, nil)
	if want == nil {
		return
	}
	if !m.tree.SelectWhere(func(c *tree.Node) bool { return c.Conn == conn && refOf(c) == *want }) {
		if !m.tree.SelectWhere(func(c *tree.Node) bool {
			return c.Conn == conn && c.Kind == tree.KindESSection && c.Section == want.section
		}) {
			m.tree.SelectWhere(func(c *tree.Node) bool { return c == root })
		}
	}
}

// indexNameMatch is the tree's name filter: a pattern with * or ?, else
// a substring, ignoring case.
func indexNameMatch(filter, name string) bool {
	f, n := strings.ToLower(filter), strings.ToLower(name)
	if strings.ContainsAny(f, "*?") {
		ok, _ := path.Match(f, n)
		return ok
	}
	return strings.Contains(n, f)
}

// healthDot is a health as a dot; red and yellow are also named, the
// row shows in the warning color.
func healthDot(h string) string {
	switch h {
	case "green":
		return "●"
	case "yellow", "red":
		return "● " + h
	}
	return "○"
}

func indexNode(conn string, i es.IndexInfo) *tree.Node {
	detail := []string{healthDot(i.Health)}
	if i.Closed() {
		detail = append(detail, "[closed]")
	} else {
		detail = append(detail, humanCount(i.Docs)+" docs", humanBytes(i.PriStoreBytes))
	}
	return &tree.Node{
		Kind: tree.KindESIndex, Conn: conn, Section: esSecIndices, Key: i.Name, Name: i.Name,
		Detail: strings.Join(detail, "  "), Warn: i.Health == "red" || i.Health == "yellow",
	}
}

// buildIndexNodes lists the shown indices, rollover groups of more than
// one index folded under their prefix (newest first), sorted by st.sortBy.
func buildIndexNodes(conn string, st *esTree) []*tree.Node {
	type item struct {
		node       *tree.Node
		docs, size int64
	}
	var shown []es.IndexInfo
	byPrefix := map[string]int{}
	for _, i := range st.indices {
		if i.System && !st.showSystem || st.filter != "" && !indexNameMatch(st.filter, i.Name) {
			continue
		}
		shown = append(shown, i)
		if p, ok := esx.RolloverPrefix(i.Name); ok {
			byPrefix[p]++
		}
	}
	slices.SortFunc(shown, func(a, b es.IndexInfo) int { return strings.Compare(b.Name, a.Name) })
	var items []*item
	groups := map[string]*item{}
	for _, i := range shown {
		p, ok := esx.RolloverPrefix(i.Name)
		if !ok || byPrefix[p] < 2 {
			items = append(items, &item{node: indexNode(conn, i), docs: i.Docs, size: i.PriStoreBytes})
			continue
		}
		g := groups[p]
		if g == nil {
			g = &item{node: &tree.Node{Kind: tree.KindIndexFolder, Conn: conn, Section: esSecIndices, Key: p}}
			groups[p] = g
			items = append(items, g)
		}
		g.node.Children = append(g.node.Children, indexNode(conn, i))
		g.docs += i.Docs
		g.size += i.PriStoreBytes
	}
	for p, g := range groups {
		g.node.Name = fmt.Sprintf("%s* (%d)", p, len(g.node.Children))
		tree.Adopt(g.node, g.node.Children)
	}
	slices.SortStableFunc(items, func(a, b *item) int {
		switch st.sortBy {
		case esByDocs:
			if c := cmp.Compare(b.docs, a.docs); c != 0 {
				return c
			}
		case esBySize:
			if c := cmp.Compare(b.size, a.size); c != 0 {
				return c
			}
		}
		return strings.Compare(a.node.Key, b.node.Key)
	})
	out := make([]*tree.Node, len(items))
	for i, it := range items {
		out[i] = it.node
	}
	return out
}

func countIndexNodes(nodes []*tree.Node) int {
	n := 0
	for _, c := range nodes {
		if c.Kind == tree.KindIndexFolder {
			n += len(c.Children)
		} else {
			n++
		}
	}
	return n
}

func countAliases(as []es.Alias) int {
	seen := map[string]bool{}
	for _, a := range as {
		seen[a.Name] = true
	}
	return len(seen)
}

// buildAliasNodes is one node per alias (as is sorted by alias), naming
// its index, or how many and the write index.
func buildAliasNodes(conn string, as []es.Alias) []*tree.Node {
	var out []*tree.Node
	for i := 0; i < len(as); {
		j := i
		var write string
		for j < len(as) && as[j].Name == as[i].Name {
			if as[j].IsWrite {
				write = as[j].Index
			}
			j++
		}
		detail := "→ " + as[i].Index
		if j-i > 1 {
			detail = fmt.Sprintf("→ %d indices", j-i)
			if write != "" {
				detail += " · write " + write
			}
		}
		out = append(out, &tree.Node{Kind: tree.KindESAlias, Conn: conn, Section: esSecAliases, Key: as[i].Name, Name: as[i].Name, Detail: detail})
		i = j
	}
	return out
}

func buildStreamNodes(conn string, ss []es.DataStream) []*tree.Node {
	out := make([]*tree.Node, len(ss))
	for i, s := range ss {
		detail := fmt.Sprintf("%d backing", len(s.Indices))
		if s.Template != "" {
			detail += " · template " + s.Template
		}
		out[i] = &tree.Node{Kind: tree.KindDataStream, Conn: conn, Section: esSecStreams, Key: s.Name, Name: s.Name, Detail: detail}
	}
	return out
}

// buildTemplateNodes are the index templates, the cluster's own only
// while system indices are shown.
func buildTemplateNodes(conn string, st *esTree) []*tree.Node {
	var out []*tree.Node
	for _, t := range st.templates {
		if t.System && !st.showSystem {
			continue
		}
		detail := t.Kind.String()
		if t.Kind != es.TemplateComponent {
			detail += " · " + strings.Join(t.Patterns, ", ") + " · p" + strconv.Itoa(t.Priority)
		}
		out = append(out, &tree.Node{
			Kind: tree.KindESTemplate, Conn: conn, Section: esSecTemplates, Key: templateKey(t), Name: t.Name, Detail: detail,
		})
	}
	return out
}

// buildPolicyNodes are the ILM policies, the cluster's own only while
// system indices are shown.
func buildPolicyNodes(conn string, st *esTree) []*tree.Node {
	var out []*tree.Node
	for _, p := range st.policies {
		if p.System && !st.showSystem {
			continue
		}
		detail := strings.Join(p.Phases, " → ")
		if p.InUse >= 0 {
			detail += " · " + indexCount(p.InUse)
		}
		out = append(out, &tree.Node{
			Kind: tree.KindESPolicy, Conn: conn, Section: esSecPolicies, Key: objKey(objPolicy, p.Name), Name: p.Name, Detail: detail,
		})
	}
	return out
}

// buildPipelineNodes are the ingest pipelines, the cluster's own only
// while system indices are shown.
func buildPipelineNodes(conn string, st *esTree) []*tree.Node {
	var out []*tree.Node
	for _, p := range st.pipelines {
		if p.System && !st.showSystem {
			continue
		}
		detail := p.Description
		if detail == "" {
			detail = fmt.Sprintf("%d processors", p.Processors)
		}
		out = append(out, &tree.Node{
			Kind: tree.KindESPipeline, Conn: conn, Section: esSecPipelines, Key: objKey(objPipeline, p.Name), Name: p.Name, Detail: detail,
		})
	}
	return out
}

// openESNode opens the Index tab of an index, alias, data stream or
// rollover group node, or an object's tab; nil for other nodes.
func (m *Model) openESNode(n *tree.Node) tea.Cmd {
	switch n.Kind {
	case tree.KindESTemplate, tree.KindESPolicy, tree.KindESPipeline:
		msg := esOpenObjectMsg{conn: n.Conn, key: n.Key}
		return func() tea.Msg { return msg }
	case tree.KindESSnapshot:
		repo, name := parseSnapshotKey(n.Key)
		msg := esOpenSnapshotMsg{conn: n.Conn, repo: repo, name: name}
		return func() tea.Msg { return msg }
	}
	msg := openIndexMsg{conn: n.Conn, target: n.Key}
	switch n.Kind {
	case tree.KindESIndex:
	case tree.KindESAlias:
		msg.kind = targetAlias
	case tree.KindDataStream:
		msg.kind = targetStream
	case tree.KindIndexFolder:
		msg.target, msg.kind = n.Key+"*", targetPattern
	default:
		return nil
	}
	return func() tea.Msg { return msg }
}

func (m *Model) esTreeAction(a keymap.Action, n *tree.Node) (tea.Cmd, bool) {
	if cmd, ok := m.esTreeWrite(a, n); ok {
		return cmd, true
	}
	st := m.esTrees[n.Conn]
	switch a {
	case keymap.ToggleInternal:
		if st != nil {
			st.showSystem = !st.showSystem
			m.buildESTree(n.Conn)
			if st.showSystem {
				m.setStatus("system indices shown", false)
			} else {
				m.setStatus("system indices hidden", false)
			}
		}
	case keymap.SortGroups:
		if st != nil {
			st.sortBy = (st.sortBy + 1) % 3
			m.buildESTree(n.Conn)
			m.setStatus("indices by "+[]string{"name", "docs", "size"}[st.sortBy], false)
		}
	case keymap.FilterPrompt:
		if st == nil || n.Section != esSecIndices {
			m.setStatus("select Indices to filter them", true)
			return nil, true
		}
		conn := n.Conn
		m.overlay = overlay.NewPrompt("Filter indices", "Name contains (or a * pattern)", st.filter, func(text string) tea.Msg {
			return esFilterTreeMsg{conn: conn, text: strings.TrimSpace(text)}
		})
	case keymap.FilterClear:
		if st != nil && st.filter != "" {
			st.filter = ""
			m.buildESTree(n.Conn)
		}
	case keymap.Refresh:
		root := m.kafkaRoot(n.Conn)
		if root == nil || root.Loading {
			return nil, true
		}
		m.tree.StartLoad(root)
		return loadESTree(m.backend, root), true
	case keymap.Open:
		if n.Kind == tree.KindIndexFolder {
			return nil, false // expands
		}
		if cmd := m.openESNode(n); cmd != nil {
			return cmd, true
		}
		return nil, false
	default:
		return nil, false
	}
	return nil, true
}

// esFilterTreeMsg sets the Indices section's name filter.
type esFilterTreeMsg struct{ conn, text string }

func (m *Model) esTreeFiltered(msg esFilterTreeMsg) {
	if st := m.esTrees[msg.conn]; st != nil {
		st.filter = msg.text
		m.buildESTree(msg.conn)
	}
}

// esTreeNames are the indices (not the system ones), aliases and data
// streams of conn's tree, once read.
func (m *Model) esTreeNames(conn string) []string {
	st := m.esTrees[conn]
	if st == nil {
		return nil
	}
	var out []string
	for _, i := range st.indices {
		if !i.System {
			out = append(out, i.Name)
		}
	}
	for _, a := range st.aliases {
		if !slices.Contains(out, a.Name) {
			out = append(out, a.Name)
		}
	}
	for _, s := range st.streams {
		out = append(out, s.Name)
	}
	return out
}

// searchES finds the indices (not the system ones), aliases and data
// streams of the loaded trees (of conn, unless empty) whose name contains
// q (lower case): searching reads nothing.
func (m *Model) searchES(conn, q string) []kafkaHit {
	var out []kafkaHit
	for _, c := range m.backend.Connections() {
		st := m.esTrees[c.Name]
		if st == nil || conn != "" && c.Name != conn || c.Driver != "elasticsearch" {
			continue
		}
		add := func(name string, kind targetKind) {
			if i := strings.Index(strings.ToLower(name), q); i >= 0 {
				out = append(out, kafkaHit{conn: c.Name, name: name, prefix: i == 0, es: true, esKind: kind})
			}
		}
		for _, i := range st.indices {
			if !i.System || st.showSystem {
				add(i.Name, targetIndex)
			}
		}
		seen := map[string]bool{}
		for _, a := range st.aliases {
			if !seen[a.Name] {
				seen[a.Name] = true
				add(a.Name, targetAlias)
			}
		}
		for _, s := range st.streams {
			add(s.Name, targetStream)
		}
	}
	return out
}
