package ui

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/rcmd"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/session"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

// Scan limits (design §4.1, §4.2): a load walks at most scanLoad keys, or
// with a filter stops at filterMatches matches or filterScanned keys
// walked; a db keeps at most maxTreeKeys keys in the tree.
const (
	scanBatch     = 1000
	scanLoad      = 10_000
	filterMatches = 500
	filterScanned = 100_000
	maxTreeKeys   = 100_000
	prodScanPause = 10 * time.Millisecond
)

var errRedisUnsupported = errors.New("not supported for redis yet")

// errRedisTx is the answer to the SQL transaction commands in a redis console.
const errRedisTx = "redis transactions: use MULTI … EXEC / DISCARD"

// redisDBKey names one db of a redis connection.
type redisDBKey struct {
	conn string
	db   int
}

// keyScan is the SCAN state behind one db node.
type keyScan struct {
	sep     string
	pattern string            // "" is no filter
	keys    map[string]string // raw key -> type
	cursor  uint64
	done    bool  // the cursor came back to 0
	capped  bool  // maxTreeKeys reached
	total   int64 // DBSIZE
	scanned int   // keys walked since the scan started, counted by COUNT
	gen     int   // bumped on every restart or cancel; late batches of an older gen are dropped
	running bool
	ctx     context.Context // the running load's
	cancel  context.CancelFunc

	loadScanned, loadMatched int // this load's counts
}

func newKeyScan(sep, pattern string) *keyScan {
	return &keyScan{sep: sep, pattern: pattern, keys: map[string]string{}, total: -1}
}

// add merges one batch (SCAN may return a key twice) and updates the counts.
func (s *keyScan) add(page rdb.ScanPage, count int) {
	s.cursor = page.Cursor
	s.scanned += count
	s.loadScanned += count
	for _, k := range page.Keys {
		if _, dup := s.keys[k.Key]; dup {
			continue
		}
		if len(s.keys) >= maxTreeKeys {
			s.capped = true
			break
		}
		s.keys[k.Key] = k.Type
		s.loadMatched++
	}
	if len(s.keys) >= maxTreeKeys {
		s.capped = true
	}
	if s.cursor == 0 {
		s.done = true
	}
}

// stop reports whether this load is over.
func (s *keyScan) stop() bool {
	switch {
	case s.done || s.capped:
		return true
	case s.pattern == "":
		return s.loadScanned >= scanLoad
	}
	return s.loadMatched >= filterMatches || s.loadScanned >= filterScanned
}

// restart forgets the keys and the cursor; the pattern stays.
func (s *keyScan) restart() {
	s.halt()
	s.keys, s.cursor, s.done, s.capped, s.scanned = map[string]string{}, 0, false, false, 0
}

// halt stops the running load; its late batches are dropped.
func (s *keyScan) halt() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.gen++
	s.running = false
}

// keyGroup collects the keys under one prefix while building the tree.
type keyGroup struct {
	prefix  string
	folders map[string]*keyGroup
	keys    []string // raw keys that end at this level
	count   int
}

// buildKeyNodes groups keys into folders by sep: at each level folders
// come first, then keys, each in raw byte order. A folder's Detail is the
// number of keys under it, "≥" while the scan is not done. expanded holds
// the raw prefixes of the folders to show open.
func buildKeyNodes(conn string, n int, keys map[string]string, sep string, done bool, expanded map[string]bool) []*tree.Node {
	root := &keyGroup{folders: map[string]*keyGroup{}}
	for k := range keys {
		g := root
		segs := rcmd.Segments(k, sep)
		for _, seg := range segs[:len(segs)-1] {
			g.count++
			next, ok := g.folders[seg]
			if !ok {
				next = &keyGroup{prefix: g.prefix + seg + sep, folders: map[string]*keyGroup{}}
				g.folders[seg] = next
			}
			g = next
		}
		g.count++
		g.keys = append(g.keys, k)
	}
	ns := db.Namespace{Database: strconv.Itoa(n)}
	var build func(g *keyGroup) []*tree.Node
	build = func(g *keyGroup) []*tree.Node {
		segs := make([]string, 0, len(g.folders))
		for s := range g.folders {
			segs = append(segs, s)
		}
		slices.Sort(segs)
		slices.Sort(g.keys)
		out := make([]*tree.Node, 0, len(segs)+len(g.keys))
		for _, s := range segs {
			f := g.folders[s]
			detail := groupDigits(int64(f.count))
			if !done {
				detail = "≥" + detail
			}
			node := &tree.Node{Kind: tree.KindKeyFolder, Name: rcmd.Escape(s), Conn: conn, NS: ns, Key: f.prefix, Detail: detail}
			tree.Adopt(node, build(f))
			node.Expanded = expanded[f.prefix]
			out = append(out, node)
		}
		for _, k := range g.keys {
			out = append(out, &tree.Node{
				Kind: tree.KindRedisKey, Name: rcmd.Escape(k[len(g.prefix):]), Conn: conn, NS: ns, Key: k, KeyType: keys[k],
			})
		}
		return out
	}
	return build(root)
}

// groupDigits writes n with thousands separators: 192569 -> "192,569".
func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// withRedis runs a read-only fn on db n of conn; a lost connection is
// reset and fn retried once, as withConn does.
func withRedis(ctx context.Context, b Backend, conn string, n int, fn func(rdb.Client) error) error {
	c, err := b.Redis(ctx, conn, n)
	if err == nil {
		err = fn(c)
	}
	if !db.IsConnLost(err) {
		return err
	}
	slog.Info("redis connection lost, reconnecting", "conn", conn, "err", err)
	b.Reset(conn)
	if c, err = b.Redis(ctx, conn, n); err != nil {
		return err
	}
	return fn(c)
}

// isRedis reports whether conn is a redis connection.
func isRedis(b Backend, conn string) bool {
	cfg, ok := b.Connection(conn)
	return ok && cfg.Driver == "redis"
}

// refuseRedis is the guard of the SQL-only actions: on a redis connection
// it says so and reports true.
func (m *Model) refuseRedis(conn string) bool {
	if conn == "" || !isRedis(m.backend, conn) {
		return false
	}
	m.setStatus(errRedisUnsupported.Error(), true)
	return true
}

// contextConn is the connection the focused pane works on.
func (m *Model) contextConn() string {
	conn, _ := m.contextNamespace()
	return conn
}

// loadRedisDBs lists a redis connection's dbs: the ones with keys first,
// then the empty ones, each by number; the configured db is always listed.
func loadRedisDBs(b Backend, n *tree.Node) tea.Cmd {
	conn := n.Conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		var dbs []rdb.DBInfo
		err := withRedis(ctx, b, conn, -1, func(c rdb.Client) error {
			var err error
			dbs, err = c.Databases(ctx)
			return err
		})
		if err != nil {
			slog.Warn("load redis dbs", "conn", conn, "err", err)
			return childrenLoadedMsg{node: n, err: err}
		}
		slices.SortStableFunc(dbs, func(a, b rdb.DBInfo) int {
			return cmp.Or(cmp.Compare(boolInt(a.Keys == 0), boolInt(b.Keys == 0)), cmp.Compare(a.Index, b.Index))
		})
		children := make([]*tree.Node, len(dbs))
		for i, d := range dbs {
			children[i] = &tree.Node{
				Kind: tree.KindRedisDB, Name: "db" + strconv.Itoa(d.Index), Conn: conn,
				NS: db.Namespace{Database: strconv.Itoa(d.Index)}, Detail: groupDigits(d.Keys),
			}
		}
		return childrenLoadedMsg{node: n, children: children}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// redisKeyOf is the db a redis tree node belongs to.
func redisKeyOf(n *tree.Node) (redisDBKey, bool) {
	switch n.Kind {
	case tree.KindRedisDB, tree.KindKeyFolder, tree.KindRedisKey, tree.KindMore:
		i, err := strconv.Atoi(n.NS.Database)
		return redisDBKey{conn: n.Conn, db: i}, err == nil
	}
	return redisDBKey{}, false
}

// redisDBNode finds the tree node of db k, or nil.
func (m *Model) redisDBNode(k redisDBKey) *tree.Node {
	want := strconv.Itoa(k.db)
	for _, r := range m.tree.Roots() {
		if r.Conn != k.conn {
			continue
		}
		for _, c := range r.Children {
			if c.Kind == tree.KindRedisDB && c.NS.Database == want {
				return c
			}
		}
	}
	return nil
}

// separator is the key separator of conn (params.separator, default ":").
func (m *Model) separator(conn string) string {
	cfg, _ := m.backend.Connection(conn)
	if s, ok := cfg.Params["separator"]; ok {
		return s
	}
	return ":"
}

// redisScanMsg is one SCAN batch of db key.
type redisScanMsg struct {
	key   redisDBKey
	gen   int
	page  rdb.ScanPage
	total int64 // -1 when not asked
	err   error
}

func (m redisScanMsg) failure() (string, error) { return m.key.conn, m.err }

// startScan opens db node n: a new scan from the first key.
func (m *Model) startScan(n *tree.Node) tea.Cmd {
	k, _ := redisKeyOf(n)
	if old := m.scans[k]; old != nil {
		old.halt()
	}
	s := newKeyScan(m.separator(k.conn), "")
	m.scans[k] = s
	return m.loadKeys(k, s)
}

// loadKeys starts one load of s: batches until stop.
func (m *Model) loadKeys(k redisDBKey, s *keyScan) tea.Cmd {
	s.halt()
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel, s.running = ctx, cancel, true
	s.loadScanned, s.loadMatched = 0, 0
	if n := m.redisDBNode(k); n != nil {
		n.Expanded = true // a load shows its keys; batches leave the node as the user has it
	}
	m.rebuildDB(k, s)
	return m.scanCmd(ctx, k, s, false)
}

// scanCmd fetches the next batch; on prod connections it waits a moment
// first so a long scan does not press the server.
func (m *Model) scanCmd(ctx context.Context, k redisDBKey, s *keyScan, pause bool) tea.Cmd {
	b, gen, cursor, match, withTotal := m.backend, s.gen, s.cursor, s.pattern, s.cursor == 0
	if cfg, _ := b.Connection(k.conn); !pause || cfg.Env != config.EnvProd {
		pause = false
	}
	return func() tea.Msg {
		if pause {
			select {
			case <-time.After(prodScanPause):
			case <-ctx.Done():
				return redisScanMsg{key: k, gen: gen, err: ctx.Err()}
			}
		}
		msg := redisScanMsg{key: k, gen: gen, total: -1}
		msg.err = withRedis(ctx, b, k.conn, k.db, func(c rdb.Client) error {
			if withTotal {
				n, err := c.DBSize(ctx)
				if err != nil {
					return err
				}
				msg.total = n
			}
			var err error
			msg.page, err = c.Scan(ctx, cursor, match, scanBatch)
			return err
		})
		return msg
	}
}

func (m *Model) scanned(msg redisScanMsg) tea.Cmd {
	s := m.scans[msg.key]
	if s == nil || msg.gen != s.gen {
		return nil // an older scan of this db
	}
	n := m.redisDBNode(msg.key)
	if n == nil {
		s.halt()
		delete(m.scans, msg.key)
		return nil
	}
	if msg.err != nil {
		s.halt()
		m.rebuildDB(msg.key, s)
		if errors.Is(msg.err, session.ErrPasswordRequired) {
			if m.overlay == nil {
				m.retryNode = n
				m.overlay = overlay.NewPassword(msg.key.conn)
			}
			return nil
		}
		if !errors.Is(msg.err, context.Canceled) {
			m.setStatus("scan "+n.Name+": "+msg.err.Error(), true)
		}
		return nil
	}
	if msg.total >= 0 {
		s.total = msg.total
		n.Detail = groupDigits(s.total)
	}
	s.add(msg.page, scanBatch)
	if s.stop() {
		s.cancel()
		s.cancel, s.running = nil, false
		m.rebuildDB(msg.key, s)
		return nil
	}
	m.rebuildDB(msg.key, s)
	return m.scanCmd(s.ctx, msg.key, s, true)
}

// rebuildDB redraws db k's subtree from s, keeping the open folders and
// the cursor.
func (m *Model) rebuildDB(k redisDBKey, s *keyScan) {
	n := m.redisDBNode(k)
	if n == nil {
		return
	}
	expanded := map[string]bool{}
	var walk func([]*tree.Node)
	walk = func(nodes []*tree.Node) {
		for _, c := range nodes {
			if c.Kind == tree.KindKeyFolder {
				if c.Expanded {
					expanded[c.Key] = true
				}
				walk(c.Children)
			}
		}
	}
	walk(n.Children)
	sel := m.tree.Selected()
	var selKind tree.Kind
	var selKey string
	reselect := sel != nil && sel.Conn == k.conn && sel.Parent() != nil && sel.NS == n.NS && sel != n
	if reselect {
		selKind, selKey = sel.Kind, sel.Key
	}

	children := buildKeyNodes(k.conn, k.db, s.keys, s.sep, s.done, expanded)
	if more := m.moreLine(s); more != "" {
		children = append(children, &tree.Node{Kind: tree.KindMore, Name: more, Conn: k.conn, NS: n.NS})
	}
	tree.Adopt(n, children)
	n.Loading, n.Err = s.running, ""
	n.Detail = groupDigits(max(s.total, 0))
	if s.pattern != "" {
		n.Detail += " · filter " + rcmd.Escape(s.pattern)
	}
	if reselect {
		m.tree.SelectWhere(func(c *tree.Node) bool {
			return c.Kind == selKind && c.Key == selKey && c.Conn == k.conn && c.NS == n.NS
		})
	}
}

// moreLine is the progress / "load more" line at the end of a db, "" when
// the scan has seen every key.
func (m *Model) moreLine(s *keyScan) string {
	scanned := groupDigits(int64(s.scanned))
	if s.total >= 0 {
		scanned = groupDigits(min(int64(s.scanned), s.total)) + " / " + groupDigits(s.total)
	}
	if s.pattern != "" {
		scanned += " · found " + groupDigits(int64(len(s.keys)))
	}
	switch {
	case s.capped:
		return joinHints("too many keys", keyHint(m.keys, keymap.Tree, keymap.FilterPrompt, "filters"))
	case s.running && s.pattern != "":
		return "scanning… scanned " + scanned
	case s.running:
		return "scanning… " + scanned
	case s.done:
		return ""
	}
	return "↓ load more (scanned " + scanned + ")"
}

// loadMore continues the scan of the db of a KindMore node.
func (m *Model) loadMore(n *tree.Node) tea.Cmd {
	k, _ := redisKeyOf(n)
	s := m.scans[k]
	if s == nil || s.running || s.done {
		return nil
	}
	if s.capped {
		m.setStatus(joinHints("too many keys", keyHint(m.keys, keymap.Tree, keymap.FilterPrompt, "filters")), true)
		return nil
	}
	return m.loadKeys(k, s)
}

// refreshKeys scans the db of n again from the start, keeping the filter.
func (m *Model) refreshKeys(n *tree.Node) tea.Cmd {
	k, _ := redisKeyOf(n)
	s := m.scans[k]
	if s == nil {
		return m.startScan(m.redisDBNode(k))
	}
	s.restart()
	return m.loadKeys(k, s)
}

// stopScans is Cancel in the tree: it stops every running scan.
func (m *Model) stopScans() bool {
	stopped := false
	for k, s := range m.scans {
		if s.running {
			s.halt()
			m.rebuildDB(k, s)
			stopped = true
		}
	}
	if stopped {
		m.setStatus("scan stopped", false)
	}
	return stopped
}

// dropScans forgets the scans of conn, whose db nodes were replaced.
func (m *Model) dropScans(conn string) {
	for k, s := range m.scans {
		if k.conn == conn {
			s.halt()
			delete(m.scans, k)
		}
	}
}
