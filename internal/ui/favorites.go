package ui

import (
	"log/slog"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/history"
	"github.com/shepherd-yun/datavim/internal/rcmd"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// favKey is a starred Redis key; key is the raw bytes.
type favKey struct {
	conn string
	db   int
	key  string
}

func (k favKey) stored() history.Favorite {
	return history.Favorite{Conn: k.conn, Database: strconv.Itoa(k.db), Name: rcmd.Quote(k.key)}
}

type (
	favoritesLoadedMsg struct {
		items []history.Favorite
		err   error
	}
	favoritesListedMsg struct {
		items []history.Favorite
		err   error
	}
	favoritePickedMsg struct{ f history.Favorite }
)

// loadFavorites reads the starred keys at start.
func loadFavorites(st Store) tea.Cmd {
	return func() tea.Msg {
		items, err := st.Favorites()
		return favoritesLoadedMsg{items: items, err: err}
	}
}

func (m *Model) favoritesLoaded(msg favoritesLoadedMsg) {
	if msg.err != nil {
		slog.Warn("load favorites", "err", msg.err)
		return
	}
	m.favorites = map[favKey]bool{}
	for _, f := range msg.items {
		n, err1 := strconv.Atoi(f.Database)
		key, err2 := parseKeyName(f.Name)
		if err1 == nil && err2 == nil {
			m.favorites[favKey{conn: f.Conn, db: n, key: key}] = true
		}
	}
	m.syncStars()
}

func (m *Model) isFavorite(k favKey) bool { return m.favorites[k] }

// syncStars marks the open key tabs that are starred.
func (m *Model) syncStars() {
	for _, t := range m.tabs {
		if kt, ok := t.(*keyTab); ok {
			kt.starred = m.isFavorite(favKey{conn: kt.conn, db: kt.db, key: kt.key})
		}
	}
}

// toggleFavorite stars or unstars k: in memory at once, in the store
// in the background.
func (m *Model) toggleFavorite(k favKey) tea.Cmd {
	if m.favorites == nil {
		m.favorites = map[favKey]bool{}
	}
	f, st := k.stored(), m.store
	var write func() error
	if m.favorites[k] {
		delete(m.favorites, k)
		m.setStatus("unstarred "+rcmd.Escape(k.key), false)
		write = func() error { return st.RemoveFavorite(f) }
	} else {
		m.favorites[k] = true
		m.setStatus("starred "+rcmd.Escape(k.key), false)
		f.Time = time.Now()
		write = func() error { return st.AddFavorite(f) }
	}
	m.syncStars()
	return func() tea.Msg {
		if err := write(); err != nil {
			slog.Warn("save favorite", "err", err)
		}
		return nil
	}
}

// toggleFavoriteHere stars the key of the focused key tab or tree node.
func (m *Model) toggleFavoriteHere() tea.Cmd {
	if m.focus == focusTabs {
		if kt, ok := m.activeTab().(*keyTab); ok {
			return m.toggleFavorite(favKey{conn: kt.conn, db: kt.db, key: kt.key})
		}
		return nil
	}
	n := m.tree.Selected()
	if n == nil || n.Kind != tree.KindRedisKey {
		m.setStatus("select a Redis key to star", true)
		return nil
	}
	k, _ := redisKeyOf(n)
	return m.toggleFavorite(favKey{conn: k.conn, db: k.db, key: n.Key})
}

// showFavorites lists the starred keys, newest first.
func (m *Model) showFavorites() tea.Cmd {
	st := m.store
	return func() tea.Msg {
		items, err := st.Favorites()
		return favoritesListedMsg{items: items, err: err}
	}
}

func (m *Model) favoritesListed(msg favoritesListedMsg) {
	switch {
	case msg.err != nil:
		m.setStatus("starred keys: "+msg.err.Error(), true)
	case len(msg.items) == 0:
		m.setStatus(joinHints("no starred keys yet", keyHint(m.keys, keymap.Tree, keymap.ToggleFavorite, "stars one")), false)
	default:
		labels := make([]string, len(msg.items))
		for i, f := range msg.items {
			labels[i] = storedKeyLabel(f.Conn, f.Database, f.Name)
		}
		items := msg.items
		m.overlay = overlay.NewPicker("Starred keys", labels, func(i int) tea.Msg { return favoritePickedMsg{f: items[i]} })
	}
}

// storedKeyLabel is "conn/db3  <escaped key>" for a key stored quoted.
func storedKeyLabel(conn, database, name string) string {
	label := name
	if key, err := parseKeyName(name); err == nil {
		label = rcmd.Escape(key)
	}
	return conn + "/db" + database + "  " + label
}

// openStoredKey opens a key kept quoted in the store (starred or recent).
func (m *Model) openStoredKey(conn, database, name string) tea.Cmd {
	if _, ok := m.backend.Connection(conn); !ok {
		m.setStatus("connection "+conn+" no longer exists", true)
		return nil
	}
	n, err := strconv.Atoi(database)
	key, kerr := parseKeyName(name)
	if err != nil || kerr != nil {
		m.setStatus("cannot read the stored key "+name, true)
		return nil
	}
	return m.openKey(conn, n, key)
}
