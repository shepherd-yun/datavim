package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/history"
	"github.com/shepherd-yun/datavim/internal/meta"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// searchLimit bounds the global search results.
const searchLimit = 200

type searchResultsMsg struct {
	gen     int
	scope   overlay.SearchScope
	hits    []meta.Hit // ScopeObjects
	kafka   []kafkaHit // ScopeObjects: topics and groups of the kafka trees loaded
	all     bool       // the results span every connection
	lines   []consoleLine
	entries []history.Entry // ScopeHistory
	err     error
}

// consoleLine is a line of an open console that matches the search.
type consoleLine struct {
	tab       int // the console's tab ID
	conn      string
	title     string
	line, col int
	text      string
}

type routineLoadedMsg struct {
	conn    string
	routine db.Routine
	ddl     string
	err     error
}

func (m routineLoadedMsg) failure() (string, error) { return m.conn, m.err }

// revealTarget is a tree path being opened for a search result, and the
// node below it a located tab wants selected once it is there.
type revealTarget struct {
	conn string
	path []string
	node *nodeTarget
}

// nodeMatch picks a tree node.
type nodeMatch func(*tree.Node) bool

// matchNode picks the node of kind whose Key is key.
func matchNode(kind tree.Kind, key string) nodeMatch {
	return func(n *tree.Node) bool { return n.Kind == kind && n.Key == key }
}

// nodeTarget is a located tab's own node: the first of matches found
// among the loaded nodes, most specific first (a later one stands in for
// a node not loaded yet).
type nodeTarget struct {
	title   string
	matches []nodeMatch
}

// treeLocator is a tab whose node is not a table or namespace path (kafka
// topics, elasticsearch indices, redis keys …). treeNodes is nil when the
// tab has no node of its own.
type treeLocator interface {
	treeNodes() []nodeMatch
}

// openSearch shows the <space><space> popup. It searches every
// connection, so it starts loading the catalogs that were never loaded;
// the connection in context is the one a leading @ narrows to.
func (m *Model) openSearch() tea.Cmd {
	conn, ns := m.contextNamespace()
	m.searchConn, m.searchHits, m.searchPicks, m.searchAll = conn, nil, nil, true
	m.searchKey, _ = metaKeyFor(m.backend, conn, ns.Database)
	var cmds []tea.Cmd
	if conn != "" {
		cmds = append(cmds, m.ensureMeta(conn, ns.Database))
	}
	for _, c := range m.backend.Connections() {
		cmds = append(cmds, m.ensureMeta(c.Name, ""))
	}
	query := m.selectionQuery()
	o := overlay.NewSearch(m.keys, m.searchNote(), conn)
	m.overlay = o
	if query != "" {
		o.Paste(query)
		cmds = append(cmds, m.runSearch(o.Requery()))
	}
	return tea.Batch(cmds...)
}

// selectionQuery takes the visual selection of the focused console as a
// search query, on one line, and leaves VISUAL mode; "" without one.
func (m *Model) selectionQuery() string {
	c, ok := m.activeTab().(*consoleTab)
	if !ok || m.focus != focusTabs || c.onResults {
		return ""
	}
	from, to, ok := c.editor.Selection()
	if !ok {
		return ""
	}
	text := c.editor.Value()[from:to]
	c.editor.Escape()
	return strings.Join(strings.Fields(text), " ")
}

// searchNote explains missing results: metadata loading or failed, of the
// connection in context in detail and of the others as counts.
func (m *Model) searchNote() string {
	var notes []string
	if n := m.searchConnNote(); n != "" {
		notes = append(notes, n)
	}
	if m.searchAll {
		if n := m.searchAllNote(); n != "" {
			notes = append(notes, n)
		}
	}
	return strings.Join(notes, " · ")
}

func (m *Model) searchConnNote() string {
	if m.searchConn == "" {
		return ""
	}
	switch _, st := m.catalogs.Get(m.searchKey); st {
	case meta.Loading:
		return "loading metadata of " + m.searchConn + "…"
	case meta.Failed:
		return joinHints("metadata of "+m.searchConn+" failed: "+m.catalogs.Err(m.searchKey).Error(), keyHint(m.keys, keymap.Global, keymap.Refresh, "retries"))
	}
	return ""
}

// searchAllNote counts the other connections whose catalog is still
// loading or failed while the search spans every connection.
func (m *Model) searchAllNote() string {
	var loading, failed int
	for _, c := range m.backend.Connections() {
		if c.Name == m.searchConn {
			continue
		}
		k, _ := metaKeyFor(m.backend, c.Name, "")
		switch _, st := m.catalogs.Get(k); st {
		case meta.Loading:
			loading++
		case meta.Failed:
			failed++
		}
	}
	var notes []string
	if loading > 0 {
		notes = append(notes, "loading metadata of "+plural(loading, "connection")+"…")
	}
	if failed > 0 {
		notes = append(notes, "metadata of "+plural(failed, "connection")+" failed")
	}
	return strings.Join(notes, " · ")
}

// runSearch answers a query of the search popup in a command. A leading @
// narrows any scope to the connection in context.
func (m *Model) runSearch(q overlay.SearchQueryMsg) tea.Cmd {
	scoped := m.searchConn != "" && strings.HasPrefix(q.Text, "@")
	text := strings.TrimPrefix(q.Text, "@")
	conn := ""
	if scoped {
		conn = m.searchConn
	}
	if q.Scope == overlay.ScopeObjects {
		return m.searchObjects(q.Gen, conn, text)
	}
	if s, ok := m.overlay.(*overlay.Search); ok {
		s.SetNote("") // the metadata notes are about the catalogs
	}
	if q.Scope == overlay.ScopeHistory {
		return searchHistory(m.store, q, conn, text)
	}
	lines := m.searchConsoles(conn, text)
	return func() tea.Msg { return searchResultsMsg{gen: q.Gen, scope: q.Scope, lines: lines} }
}

// searchObjects matches text against the catalogs. With no connection
// given it searches every connection, loading the catalogs that were never
// loaded (a PostgreSQL connection's default database).
func (m *Model) searchObjects(gen int, conn, text string) tea.Cmd {
	all := conn == ""
	var loads []tea.Cmd
	if all {
		for _, c := range m.backend.Connections() {
			loads = append(loads, m.ensureMeta(c.Name, ""))
		}
	}
	m.searchAll = all
	if s, ok := m.overlay.(*overlay.Search); ok {
		s.SetNote(m.searchNote())
	}
	snaps := m.catalogs.Snapshots(conn)
	kafka := m.searchKafka(conn, text)
	return tea.Batch(append(loads, func() tea.Msg {
		return searchResultsMsg{gen: gen, hits: meta.Search(snaps, text, searchLimit), kafka: kafka, all: all}
	})...)
}

// searchConsoles finds the lines of the open consoles (of conn, unless
// empty) that contain text, ignoring case.
func (m *Model) searchConsoles(conn, text string) []consoleLine {
	q := strings.ToLower(strings.TrimSpace(text))
	if q == "" {
		return nil
	}
	var out []consoleLine
	for _, t := range m.tabs {
		c, ok := t.(*consoleTab)
		if !ok || conn != "" && c.conn != conn {
			continue
		}
		for i, line := range strings.Split(c.editor.Value(), "\n") {
			lower := strings.ToLower(line)
			at := strings.Index(lower, q)
			if at < 0 {
				continue
			}
			out = append(out, consoleLine{
				tab: c.ID(), conn: c.conn, title: c.Title(), line: i,
				col: utf8.RuneCountInString(lower[:at]), text: line,
			})
			if len(out) == searchLimit {
				return out
			}
		}
	}
	return out
}

// searchHistory finds the statements (of conn, unless empty) that contain
// text; an empty text lists the recent ones.
func searchHistory(st Store, q overlay.SearchQueryMsg, conn, text string) tea.Cmd {
	return func() tea.Msg {
		entries, err := st.History(strings.TrimSpace(text), searchLimit)
		if conn != "" {
			kept := entries[:0]
			for _, e := range entries {
				if e.Conn == conn {
					kept = append(kept, e)
				}
			}
			entries = kept
		}
		return searchResultsMsg{gen: q.Gen, scope: q.Scope, entries: entries, err: err}
	}
}

func (m *Model) searchResults(msg searchResultsMsg) {
	s, ok := m.overlay.(*overlay.Search)
	if !ok {
		return
	}
	if msg.err != nil {
		s.SetNote("history: " + msg.err.Error())
	}
	var items []overlay.SearchItem
	var picks []func(bool) tea.Cmd
	switch msg.scope {
	case overlay.ScopeObjects:
		for _, h := range msg.kafka {
			if h.prefix {
				items = append(items, h.item())
				picks = append(picks, func(bool) tea.Cmd { return m.openKafkaHit(h) })
			}
		}
		for _, h := range msg.hits {
			items = append(items, searchItem(h, msg.all))
			picks = append(picks, func(structure bool) tea.Cmd { return m.openHit(h, structure) })
		}
		for _, h := range msg.kafka {
			if !h.prefix {
				items = append(items, h.item())
				picks = append(picks, func(bool) tea.Cmd { return m.openKafkaHit(h) })
			}
		}
	case overlay.ScopeConsoles:
		for _, l := range msg.lines {
			items = append(items, overlay.SearchItem{
				Kind: "console", Label: strings.TrimSpace(l.text),
				Where: fmt.Sprintf("%s · %s · line %d", l.conn, l.title, l.line+1),
			})
			picks = append(picks, func(bool) tea.Cmd { return m.gotoConsoleLine(l) })
		}
	case overlay.ScopeHistory:
		for _, e := range msg.entries {
			items = append(items, historyItem(e))
			picks = append(picks, func(bool) tea.Cmd { return m.useHistory(e) })
		}
	}
	if s.SetResults(msg.gen, items) {
		m.searchHits, m.searchPicks = msg.hits, picks
	}
}

func historyItem(e history.Entry) overlay.SearchItem {
	where := e.Conn
	if e.Database != "" {
		where += "/" + e.Database
	}
	where += " · " + e.Time.Local().Format("01-02 15:04")
	if e.Err != "" {
		where += " · failed"
	}
	return overlay.SearchItem{Kind: "history", Label: strings.Join(strings.Fields(e.SQL), " "), Where: where}
}

// gotoConsoleLine switches to the console of l with the cursor on the match.
func (m *Model) gotoConsoleLine(l consoleLine) tea.Cmd {
	for i, t := range m.tabs {
		if c, ok := t.(*consoleTab); ok && c.ID() == l.tab {
			m.gotoTab(i)
			c.onResults = false
			c.editor.SetCursor(l.line, l.col)
			return nil
		}
	}
	m.setStatus("that console was closed", true)
	return nil
}

func searchItem(h meta.Hit, all bool) overlay.SearchItem {
	var where []string
	if all {
		where = append(where, h.Conn)
	}
	switch h.Kind {
	case meta.ItemDatabase:
	case meta.ItemSchema:
		where = append(where, h.NS.Database)
	default:
		for _, p := range []string{h.NS.Database, h.NS.Schema} {
			if p != "" {
				where = append(where, p)
			}
		}
	}
	w := strings.Join(where, "/")
	if h.Detail != "" {
		if w != "" {
			w += " · "
		}
		w += h.Detail
	}
	return overlay.SearchItem{Kind: h.Kind.String(), Label: h.Path, Where: w}
}

func (m *Model) searchPicked(msg overlay.SearchPickMsg) tea.Cmd {
	if msg.Index < 0 || msg.Index >= len(m.searchPicks) {
		return nil
	}
	return m.searchPicks[msg.Index](msg.Structure)
}

// openHit opens a catalog result: a table's data (or its structure),
// a column's table with the cursor on the column, a routine's definition,
// or a database or schema in the tree.
func (m *Model) openHit(h meta.Hit, structure bool) tea.Cmd {
	switch h.Kind {
	case meta.ItemTable, meta.ItemView, meta.ItemColumn:
		ref := db.TableRef{Namespace: h.NS, Name: h.Table}
		if structure {
			return m.openStructure(h.Conn, ref)
		}
		cmd := m.openData(h.Conn, ref)
		if t, ok := m.activeTab().(*dataTab); ok && h.Kind == meta.ItemColumn {
			t.focusColumn(h.Name)
		}
		return cmd
	case meta.ItemRoutine:
		return loadRoutine(m.backend, h.Conn, h.Routine)
	case meta.ItemDatabase:
		return m.reveal(h.Conn, []string{h.NS.Database})
	case meta.ItemSchema:
		return m.reveal(h.Conn, nsPath(h.NS))
	}
	return nil
}

func loadRoutine(b Backend, conn string, r db.Routine) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		var ddl string
		err := withConn(ctx, b, conn, r.Namespace.Database, func(c db.Conn) error {
			var err error
			ddl, err = c.Meta().RoutineDDL(ctx, r)
			return err
		})
		return routineLoadedMsg{conn: conn, routine: r, ddl: ddl, err: err}
	}
}

func (m *Model) routineLoaded(msg routineLoadedMsg) {
	if msg.err != nil {
		m.setStatus(msg.routine.Name+": "+msg.err.Error(), true)
		return
	}
	m.overlay = overlay.NewDetail(msg.routine.Name, msg.ddl)
}

// reveal expands the tree along path and selects its end; children that
// are not loaded yet are loaded on the way (continueReveal resumes).
func (m *Model) reveal(conn string, path []string) tea.Cmd {
	return m.revealNode(conn, path, nil)
}

// revealNode is reveal, then the selection of node (when not nil) below
// the path.
func (m *Model) revealNode(conn string, path []string, node *nodeTarget) tea.Cmd {
	m.setFocus(focusTree)
	m.revealing = nil
	n, res := m.tree.Reveal(conn, path)
	switch res {
	case tree.NeedsLoad:
		m.revealing = &revealTarget{conn: conn, path: path, node: node}
		return loadChildren(m.backend, n)
	case tree.Waiting:
		m.revealing = &revealTarget{conn: conn, path: path, node: node}
	case tree.NotFound:
		m.setStatus(strings.Join(path, "/")+" is not in the tree of "+conn, true)
	case tree.Revealed:
		if node != nil {
			m.selectNode(conn, node)
		}
	}
	return nil
}

// selectNode selects the first of node's matches in conn's loaded tree.
func (m *Model) selectNode(conn string, node *nodeTarget) {
	for i, match := range node.matches {
		if m.tree.RevealWhere(conn, match) {
			if i > 0 {
				m.setStatus(node.title+" is not loaded in the tree yet: its parent is selected", false)
			}
			return
		}
	}
	m.setStatus(node.title+" is not in the tree of "+conn+" (filtered, hidden or not loaded)", true)
}

// locate reveals what the active tab shows in the tree: its table, its
// own node (a kafka topic, an elasticsearch index, a redis key …), or the
// namespace (else the connection) of a console.
func (m *Model) locate() tea.Cmd {
	t := m.activeTab()
	if t == nil {
		m.setStatus("no tab to locate", true)
		return nil
	}
	conn, ref := t.Target()
	var node *nodeTarget
	if l, ok := t.(treeLocator); ok {
		if ms := l.treeNodes(); len(ms) > 0 {
			node = &nodeTarget{title: t.Title(), matches: ms}
		}
	}
	if k, ok := t.(*keyTab); ok {
		return m.revealNode(conn, []string{"db" + strconv.Itoa(k.db)}, node)
	}
	path := nsPath(ref.Namespace)
	if ref.Name != "" && len(path) > 0 {
		path = append(path, ref.Name)
	}
	return m.revealNode(conn, path, node)
}

// continueReveal resumes a reveal once children of its connection loaded.
func (m *Model) continueReveal(msg childrenLoadedMsg) tea.Cmd {
	r := m.revealing
	if r == nil || r.conn != msg.node.Conn {
		return nil
	}
	if msg.err != nil {
		m.revealing = nil
		return nil
	}
	return m.revealNode(r.conn, r.path, r.node)
}
