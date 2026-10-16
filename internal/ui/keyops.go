package ui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/rcmd"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

// keyOp is a one-step change to a whole key (design §6.1, last row).
type keyOp int

const (
	keyOpTTL keyOp = iota
	keyOpRename
	keyOpDelete
	keyOpNew
)

// keyOpReq is one key operation on its way: asked for, then run.
type keyOpReq struct {
	op        keyOp
	conn      string
	db        int
	key, to   string        // to: the new name of a rename
	typ       string        // the key's type, for the tree
	ttl       time.Duration // keyOpTTL: < 0 removes it
	overwrite bool          // keyOpRename: RENAME over an existing key
}

type (
	// keyOpAskMsg is t, r or D in a key tab: the root model asks for it.
	keyOpAskMsg struct{ req keyOpReq }
	// keyOpInputMsg is the text typed for a TTL or a new name.
	keyOpInputMsg struct {
		req  keyOpReq
		text string
	}
	// keyOpRunMsg is a confirmed key operation.
	keyOpRunMsg struct{ req keyOpReq }
	// keyOpMsg is a finished key operation.
	keyOpMsg struct {
		req keyOpReq
		ok  bool // false: Rename found to existing / Expire found the key gone
		err error
	}
)

func (m keyOpMsg) failure() (string, error) { return m.req.conn, m.err }

// parseKeyName reads one key name typed as redis-cli takes it.
func parseKeyName(s string) (string, error) {
	args, err := rcmd.Split(s)
	switch {
	case err != nil:
		return "", err
	case len(args) == 0:
		return "", errors.New("type a key name")
	case len(args) > 1:
		return "", errors.New(`quote a name with spaces, e.g. "a b"`)
	}
	return args[0], nil
}

var errTTL = errors.New("type seconds (a whole number), or -1 to remove the TTL")

// parseTTL reads the TTL prompt: seconds, or -1 / empty for none.
func parseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "-1" {
		return -1, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || n > int64(time.Duration(1<<63-1)/time.Second) {
		return 0, errTTL
	}
	return time.Duration(n) * time.Second, nil
}

// keyTabOf is the open detail tab of key, or nil.
func (m *Model) keyTabOf(conn string, n int, key string) *keyTab {
	want := keyTabKey(conn, n, key)
	for _, t := range m.tabs {
		if kt, ok := t.(*keyTab); ok && kt.Key() == want {
			return kt
		}
	}
	return nil
}

// keyOpBlocked says why key cannot be changed now ("" when it can): a
// read-only connection, or its tab has staged changes.
func (m *Model) keyOpBlocked(conn string, n int, key string) string {
	if cfg, _ := m.backend.Connection(conn); cfg.IsReadOnly() {
		return readOnlyHint
	}
	if kt := m.keyTabOf(conn, n, key); kt != nil && kt.changes() > 0 {
		return kt.unsavedNote()
	}
	return ""
}

func (m *Model) isProd(conn string) bool {
	cfg, _ := m.backend.Connection(conn)
	return cfg.Env == config.EnvProd
}

// askTTL asks for the TTL of key, starting from ttl (< 0: none).
func (m *Model) askTTL(conn string, n int, key string, ttl time.Duration) tea.Cmd {
	if msg := m.keyOpBlocked(conn, n, key); msg != "" {
		m.setStatus(msg, true)
		return nil
	}
	initial := ""
	if ttl >= 0 {
		initial = strconv.FormatInt(int64(ttl/time.Second), 10)
	}
	req := keyOpReq{op: keyOpTTL, conn: conn, db: n, key: key}
	m.overlay = overlay.NewPrompt("TTL of "+rcmd.Escape(key), "Seconds · -1 or empty removes the TTL", initial,
		func(s string) tea.Msg { return keyOpInputMsg{req: req, text: s} },
		overlay.WithValidate(func(s string) error { _, err := parseTTL(s); return err }))
	return nil
}

// askRenameKey asks for key's new name.
func (m *Model) askRenameKey(conn string, n int, key, typ string) tea.Cmd {
	if msg := m.keyOpBlocked(conn, n, key); msg != "" {
		m.setStatus(msg, true)
		return nil
	}
	req := keyOpReq{op: keyOpRename, conn: conn, db: n, key: key, typ: typ}
	m.overlay = overlay.NewPrompt("Rename "+rcmd.Escape(key), "New name (quote it as in redis-cli when needed)", rcmd.Quote(key),
		func(s string) tea.Msg { return keyOpInputMsg{req: req, text: s} },
		overlay.WithValidate(func(s string) error { _, err := parseKeyName(s); return err }))
	return nil
}

// askDeleteKey confirms deleting key: y, or the connection's name on prod.
func (m *Model) askDeleteKey(conn string, n int, key, typ string) tea.Cmd {
	if msg := m.keyOpBlocked(conn, n, key); msg != "" {
		m.setStatus(msg, true)
		return nil
	}
	expect := ""
	if m.isProd(conn) {
		expect = conn
	}
	what := rcmd.Escape(key)
	if typ != "" {
		what += " (" + typ + ")"
	}
	req := keyOpReq{op: keyOpDelete, conn: conn, db: n, key: key, typ: typ}
	m.overlay = overlay.NewConfirm("Delete key", []string{"Delete " + what + "?", "This cannot be undone."}, expect, keyOpRunMsg{req: req})
	return nil
}

// keyOpInput turns the typed TTL or name into the operation, confirmed
// on prod.
func (m *Model) keyOpInput(msg keyOpInputMsg) tea.Cmd {
	req := msg.req
	var cmds [][]string
	switch req.op {
	case keyOpTTL:
		ttl, err := parseTTL(msg.text)
		if err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		req.ttl = ttl
		cmds = [][]string{{"PERSIST", req.key}}
		if ttl >= 0 {
			cmds = [][]string{{"EXPIRE", req.key, strconv.FormatInt(int64(ttl/time.Second), 10)}}
		}
	case keyOpRename:
		to, err := parseKeyName(msg.text)
		if err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		if to == req.key {
			return nil
		}
		req.to = to
		cmds = [][]string{{"RENAMENX", req.key, to}}
	}
	return m.guardKeyOp(req, cmds)
}

// guardKeyOp checks sqlvim's commands for req: refused on read-only,
// confirmed with y on prod, run at once otherwise.
func (m *Model) guardKeyOp(req keyOpReq, cmds [][]string) tea.Cmd {
	cfg, _ := m.backend.Connection(req.conn)
	chk := rcmd.GuardArgs(cmds, rcmd.Policy{ReadOnly: cfg.IsReadOnly(), Prod: cfg.Env == config.EnvProd, Conn: req.conn, DBSize: -1})
	switch chk.Verdict {
	case rcmd.Reject:
		m.setStatus(chk.Reason, true)
		return nil
	case rcmd.Confirm:
		m.overlay = overlay.NewConfirm(chk.Title, chk.Body, chk.Expect, keyOpRunMsg{req: req})
		return nil
	}
	return m.runKeyOp(req)
}

// runKeyOp runs a confirmed operation.
func (m *Model) runKeyOp(req keyOpReq) tea.Cmd {
	b := m.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		out := keyOpMsg{req: req}
		out.err = withRedis(ctx, b, req.conn, req.db, func(c rdb.Client) error {
			var err error
			switch req.op {
			case keyOpTTL:
				out.ok, err = c.Expire(ctx, req.key, req.ttl)
			case keyOpRename:
				out.ok, err = c.Rename(ctx, req.key, req.to, req.overwrite)
			case keyOpDelete:
				var n int64
				n, err = c.Unlink(ctx, req.key)
				out.ok = n > 0
			}
			return err
		})
		return out
	}
}

// keyOpDone reports a finished operation and brings the tree and the
// key's tab up to date.
func (m *Model) keyOpDone(msg keyOpMsg) tea.Cmd {
	req := msg.req
	name := rcmd.Escape(req.key)
	if msg.err != nil {
		m.setStatus(name+": "+msg.err.Error(), true)
		return nil
	}
	reload := func(key string) tea.Cmd {
		if kt := m.keyTabOf(req.conn, req.db, key); kt != nil && !kt.loading {
			return kt.load(true)
		}
		return nil
	}
	switch req.op {
	case keyOpTTL:
		switch {
		case !msg.ok:
			m.setStatus("key no longer exists", true)
		case req.ttl < 0:
			m.setStatus("TTL removed", false)
		default:
			m.setStatus("TTL set to "+strconv.FormatInt(int64(req.ttl/time.Second), 10)+"s", false)
		}
		return reload(req.key)
	case keyOpRename:
		if !msg.ok {
			expect := ""
			if m.isProd(req.conn) {
				expect = req.conn
			}
			req.overwrite = true
			m.overlay = overlay.NewConfirm("Overwrite key", []string{
				rcmd.Escape(req.to) + " already exists · overwrite it?", "Its value is lost.",
			}, expect, keyOpRunMsg{req: req})
			return nil
		}
		m.setStatus("renamed "+name+" to "+rcmd.Escape(req.to), false)
		m.keyChanged(redisKeyChangedMsg{conn: req.conn, db: req.db, key: req.key})
		m.keyChanged(redisKeyChangedMsg{conn: req.conn, db: req.db, key: req.to, typ: req.typ})
		return m.retargetKeyTab(req.conn, req.db, req.key, req.to)
	case keyOpDelete:
		m.setStatus("deleted "+name, false)
		m.keyChanged(redisKeyChangedMsg{conn: req.conn, db: req.db, key: req.key})
		return reload(req.key)
	}
	return nil
}

// retargetKeyTab points the open tab of from at to and reloads it; a tab
// already open on to is closed first (to's old value is gone).
func (m *Model) retargetKeyTab(conn string, n int, from, to string) tea.Cmd {
	kt := m.keyTabOf(conn, n, from)
	if kt == nil {
		if other := m.keyTabOf(conn, n, to); other != nil && !other.loading {
			return other.load(true)
		}
		return nil
	}
	var closed tea.Cmd
	if other := m.keyTabOf(conn, n, to); other != nil {
		for i, t := range m.tabs {
			if t == other {
				closed = m.closeTab(i)
				break
			}
		}
	}
	kt.key = to
	return tea.Batch(closed, kt.load(true))
}
