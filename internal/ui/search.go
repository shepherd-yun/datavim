package ui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/meta"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

// searchLimit bounds the global search results.
const searchLimit = 200

type searchResultsMsg struct {
	gen  int
	hits []meta.Hit
	all  bool // the results span every connection
}

type routineLoadedMsg struct {
	conn    string
	routine db.Routine
	ddl     string
	err     error
}

func (m routineLoadedMsg) failure() (string, error) { return m.conn, m.err }

// revealTarget is a tree path being opened for a search result.
type revealTarget struct {
	conn string
	path []string
}

// openSearch shows the <space><space> popup for the connection in context.
func (m *Model) openSearch() tea.Cmd {
	conn, ns := m.contextNamespace()
	m.searchConn, m.searchHits, m.searchAll = conn, nil, false
	m.searchKey, _ = metaKeyFor(m.backend, conn, ns.Database)
	var cmd tea.Cmd
	if conn != "" {
		cmd = m.ensureMeta(conn, ns.Database)
	}
	m.overlay = overlay.NewSearch(m.keys, m.searchNote())
	return cmd
}

// searchNote explains missing results: metadata loading or failed.
func (m *Model) searchNote() string {
	if m.searchAll {
		return m.searchAllNote()
	}
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

// searchAllNote counts the connections whose catalog is still loading or
// failed while the search spans every connection.
func (m *Model) searchAllNote() string {
	var loading, failed int
	for _, c := range m.backend.Connections() {
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

// runSearch matches text in a command. A leading @, or no connection in
// context, searches the catalogs of every connection; the first such
// search loads the catalogs that were never loaded (a PostgreSQL
// connection's default database).
func (m *Model) runSearch(text string, gen int) tea.Cmd {
	all := m.searchConn == "" || strings.HasPrefix(text, "@")
	conn := m.searchConn
	var loads []tea.Cmd
	if all {
		conn = ""
		for _, c := range m.backend.Connections() {
			loads = append(loads, m.ensureMeta(c.Name, ""))
		}
	}
	if all != m.searchAll {
		m.searchAll = all
		if s, ok := m.overlay.(*overlay.Search); ok {
			s.SetNote(m.searchNote())
		}
	}
	snaps := m.catalogs.Snapshots(conn)
	q := strings.TrimPrefix(text, "@")
	return tea.Batch(append(loads, func() tea.Msg {
		return searchResultsMsg{gen: gen, hits: meta.Search(snaps, q, searchLimit), all: all}
	})...)
}

func (m *Model) searchResults(msg searchResultsMsg) {
	s, ok := m.overlay.(*overlay.Search)
	if !ok {
		return
	}
	items := make([]overlay.SearchItem, len(msg.hits))
	for i, h := range msg.hits {
		items[i] = searchItem(h, msg.all)
	}
	if s.SetResults(msg.gen, items) {
		m.searchHits = msg.hits
	}
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

// searchPicked opens a result: a table's data (or its structure),
// a column's table with the cursor on the column, a routine's definition,
// or a database or schema in the tree.
func (m *Model) searchPicked(msg overlay.SearchPickMsg) tea.Cmd {
	if msg.Index < 0 || msg.Index >= len(m.searchHits) {
		return nil
	}
	h := m.searchHits[msg.Index]
	switch h.Kind {
	case meta.ItemTable, meta.ItemView, meta.ItemColumn:
		ref := db.TableRef{Namespace: h.NS, Name: h.Table}
		if msg.Structure {
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
		return m.reveal(h.Conn, []string{h.NS.Database, h.NS.Schema})
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
	m.setFocus(focusTree)
	m.revealing = nil
	n, res := m.tree.Reveal(conn, path)
	switch res {
	case tree.NeedsLoad:
		m.revealing = &revealTarget{conn: conn, path: path}
		return loadChildren(m.backend, n)
	case tree.Waiting:
		m.revealing = &revealTarget{conn: conn, path: path}
	case tree.NotFound:
		m.setStatus(strings.Join(path, "/")+" is not in the tree of "+conn, true)
	}
	return nil
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
	return m.reveal(r.conn, r.path)
}
