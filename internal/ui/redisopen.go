package ui

import (
	"context"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/rcmd"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/complete"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

// maxKeyCandidates caps the completion list of the open-key prompt.
const maxKeyCandidates = 50

// redisFilterMsg is the filter typed for db key ("" clears it).
type redisFilterMsg struct {
	key  redisDBKey
	text string
}

// openKeyMsg asks to open key by name; it is checked to exist first.
type openKeyMsg struct {
	conn string
	db   int
	key  string
}

type keyCheckedMsg struct {
	openKeyMsg
	exists bool
	err    error
}

func (m keyCheckedMsg) failure() (string, error) { return m.conn, m.err }

// filterKeysPrompt is F on a redis db, folder or key: it asks for the
// pattern, starting from the current one (or the folder's prefix).
func (m *Model) filterKeysPrompt() tea.Cmd {
	n := m.tree.Selected()
	if n == nil {
		return nil
	}
	k, ok := redisKeyOf(n)
	if !ok {
		return nil
	}
	initial := ""
	if s := m.scans[k]; s != nil {
		initial = s.pattern
	}
	if initial == "" && n.Kind == tree.KindKeyFolder {
		initial = rcmd.Escape(n.Key) + "*"
	}
	m.overlay = overlay.NewPrompt("Filter keys", "Pattern", initial, func(text string) tea.Msg {
		return redisFilterMsg{key: k, text: text}
	})
	return nil
}

// filterKeys scans db k again with the pattern made from text; blank
// text clears the filter.
func (m *Model) filterKeys(k redisDBKey, text string) tea.Cmd {
	if m.redisDBNode(k) == nil {
		return nil
	}
	s := m.scans[k]
	if s == nil {
		s = newKeyScan(m.separator(k.conn), "")
		m.scans[k] = s
	}
	s.pattern = rcmd.Pattern(text)
	s.restart()
	return m.loadKeys(k, s)
}

// redisContext is the redis db the focused pane is on: the selected tree
// node's db, or a connection's default db.
func (m *Model) redisContext() (redisDBKey, bool) {
	n := m.tree.Selected()
	if n == nil {
		return redisDBKey{}, false
	}
	if k, ok := redisKeyOf(n); ok {
		return k, true
	}
	if n.Kind == tree.KindConn && isRedis(m.backend, n.Conn) {
		cfg, _ := m.backend.Connection(n.Conn)
		return redisDBKey{conn: n.Conn, db: config.RedisDB(cfg)}, true
	}
	return redisDBKey{}, false
}

// openKeyPrompt is <space>k: it asks for a key name, completing the keys
// already scanned in that db.
func (m *Model) openKeyPrompt() tea.Cmd {
	k, ok := m.redisContext()
	if !ok {
		m.setStatus("select a redis connection first", true)
		return nil
	}
	m.overlay = overlay.NewPrompt("Open key", "Key", "", func(text string) tea.Msg {
		return openKeyMsg{conn: k.conn, db: k.db, key: text}
	}, overlay.WithCompleter(func(text string, _ int) (int, []complete.Candidate) {
		return 0, m.keyCandidates(k, text)
	}))
	return nil
}

// keyCandidates are the scanned keys of db k that start with prefix, in
// byte order.
func (m *Model) keyCandidates(k redisDBKey, prefix string) []complete.Candidate {
	s := m.scans[k]
	if s == nil {
		return nil
	}
	var keys []string
	for key := range s.keys {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	keys = keys[:min(len(keys), maxKeyCandidates)]
	out := make([]complete.Candidate, len(keys))
	for i, key := range keys {
		out[i] = complete.Candidate{Text: key, Label: rcmd.Escape(key), Kind: complete.Keyword}
	}
	return out
}

// checkKey looks the typed key up; the name is used as typed.
func checkKey(b Backend, msg openKeyMsg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := keyCheckedMsg{openKeyMsg: msg}
		out.err = withRedis(ctx, b, msg.conn, msg.db, func(c rdb.Client) error {
			info, err := c.KeyInfo(ctx, msg.key)
			out.exists = info.Exists
			return err
		})
		return out
	}
}

func (m *Model) keyChecked(msg keyCheckedMsg) tea.Cmd {
	switch {
	case msg.err != nil:
		m.setStatus(msg.err.Error(), true)
	case !msg.exists:
		m.setStatus("key not found: "+rcmd.Escape(msg.key), true)
	default:
		return m.openKey(msg.conn, msg.db, msg.key)
	}
	return nil
}
