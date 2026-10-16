package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

// newRedisModel is a model whose only connection is the redis "cache".
func newRedisModel(t *testing.T, cfg config.Connection) (*Model, *fakeBackend, *fakeRedis) {
	t.Helper()
	b := newFakeBackend()
	b.conns = nil
	if cfg.Name == "" {
		cfg.Name = "cache"
	}
	r := b.addRedis(cfg)
	return newTestModelWith(t, b), b, r
}

// drainAll is drain without its step limit, for scans of many batches.
func drainAll(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 100_000 {
			t.Fatal("drainAll: too many steps")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		default:
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
}

func pressAll(t *testing.T, m *Model, keys ...string) {
	t.Helper()
	for _, k := range keys {
		_, cmd := m.Update(uitest.Key(k))
		drainAll(t, m, cmd)
	}
}

// countKeys counts the key nodes under n.
func countKeys(n *tree.Node) int {
	c := 0
	for _, ch := range n.Children {
		switch ch.Kind {
		case tree.KindRedisKey:
			c++
		case tree.KindKeyFolder:
			c += countKeys(ch)
		}
	}
	return c
}

func moreNode(n *tree.Node) *tree.Node {
	if len(n.Children) > 0 && n.Children[len(n.Children)-1].Kind == tree.KindMore {
		return n.Children[len(n.Children)-1]
	}
	return nil
}

// expandDB0 expands the connection and then db0 (the first db with keys).
func expandDB0(t *testing.T, m *Model) *tree.Node {
	t.Helper()
	m.setFocus(focusTree)
	pressAll(t, m, "l", "j", "l")
	n := m.tree.Selected()
	if n == nil || n.Kind != tree.KindRedisDB || n.Name != "db0" {
		t.Fatalf("selected %+v, want db0\n%s", n, screen(m))
	}
	return n
}

func TestBuildKeyNodes(t *testing.T) {
	keys := map[string]string{"user:1": "string", "user:2": "hash", "user:9:sess": "string", "config": "list"}
	nodes := buildKeyNodes("cache", 0, keys, ":", false, nil)
	if len(nodes) != 2 || nodes[0].Kind != tree.KindKeyFolder || nodes[0].Name != "user" || nodes[0].Detail != "≥3" ||
		nodes[1].Kind != tree.KindRedisKey || nodes[1].Name != "config" || nodes[1].KeyType != "list" {
		t.Fatalf("top level = %v", describeNodes(nodes))
	}
	user := nodes[0].Children
	if len(user) != 3 || user[0].Name != "9" || user[0].Detail != "≥1" || user[1].Name != "1" || user[2].Name != "2" ||
		user[1].Key != "user:1" || nodes[0].Key != "user:" {
		t.Fatalf("user = %v", describeNodes(user))
	}
	if nodes[0].Expanded {
		t.Fatal("folders start closed")
	}
	nodes = buildKeyNodes("cache", 0, keys, ":", true, map[string]bool{"user:": true})
	if !nodes[0].Expanded || nodes[0].Detail != "3" {
		t.Fatalf("done/expanded: %+v", nodes[0])
	}
	nodes = buildKeyNodes("cache", 0, map[string]string{"\xff:a": "string"}, ":", true, nil)
	if nodes[0].Name != `\xFF` || nodes[0].Children[0].Key != "\xff:a" {
		t.Fatalf("binary: %v", describeNodes(nodes))
	}
	nodes = buildKeyNodes("cache", 0, map[string]string{"a": "string", "a:b": "string"}, ":", true, nil)
	if len(nodes) != 2 || nodes[0].Kind != tree.KindKeyFolder || nodes[1].Kind != tree.KindRedisKey {
		t.Fatalf("key and prefix: %v", describeNodes(nodes))
	}
}

func describeNodes(nodes []*tree.Node) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.Name+"("+n.Detail+")")
	}
	return out
}

func TestKeyScanStop(t *testing.T) {
	next := 0
	page := func(n int, cursor uint64, match bool) rdb.ScanPage {
		p := rdb.ScanPage{Cursor: cursor}
		if match {
			for range n {
				next++
				p.Keys = append(p.Keys, rdb.KeyEntry{Key: fmt.Sprint("x", next), Type: "string"})
			}
		}
		return p
	}
	s := newKeyScan(":", "")
	for range 9 {
		s.add(page(0, 1, false), 1000)
		if s.stop() {
			t.Fatal("stopped before 10,000 scanned")
		}
	}
	s.add(page(0, 1, false), 1000)
	if !s.stop() {
		t.Fatal("unfiltered load goes on past 10,000")
	}

	s = newKeyScan(":", "*x*")
	s.add(page(499, 1, true), 1000)
	if s.stop() {
		t.Fatal("stopped at 499 matches")
	}
	s.add(page(1, 1, true), 1000)
	if s.loadMatched < 500 || !s.stop() {
		t.Fatalf("filter goes on at %d matches", s.loadMatched)
	}

	s = newKeyScan(":", "*nomatch*")
	for range 99 {
		s.add(page(0, 1, false), 1000)
	}
	if s.stop() {
		t.Fatal("filter stopped before 100,000 scanned")
	}
	s.add(page(0, 1, false), 1000)
	if !s.stop() {
		t.Fatal("filter goes on past 100,000 scanned")
	}

	s = newKeyScan(":", "")
	s.add(page(0, 0, false), 1000)
	if !s.done || !s.stop() {
		t.Fatal("cursor 0 ends the scan")
	}

	s = newKeyScan(":", "")
	for i := range maxTreeKeys {
		s.keys[fmt.Sprint("k", i)] = "string"
	}
	s.add(rdb.ScanPage{Cursor: 5, Keys: []rdb.KeyEntry{{Key: "new", Type: "string"}}}, 1000)
	if !s.capped || !s.stop() || len(s.keys) != maxTreeKeys {
		t.Fatalf("capped=%v keys=%d", s.capped, len(s.keys))
	}
}

func TestRedisTreeExpand(t *testing.T) {
	m, b, r := newRedisModel(t, config.Connection{})
	r.fill(0, "user:", 2500)
	dbn := expandDB0(t, m)
	if got := countKeys(dbn); got != 2500 {
		t.Fatalf("keys = %d", got)
	}
	if moreNode(dbn) != nil || dbn.Loading {
		t.Fatalf("finished scan still shows progress: %+v", moreNode(dbn))
	}
	scans := r.scanCalls()
	if len(scans) != 3 {
		t.Fatalf("scans = %+v", scans)
	}
	for _, s := range scans {
		if s.count != 1000 || s.db != 0 {
			t.Fatalf("scan = %+v", s)
		}
	}
	if !strings.Contains(screen(m), "2,500") {
		t.Fatalf("db key count not shown:\n%s", screen(m))
	}
	if b.connCalls != 0 {
		t.Fatalf("a redis tree made %d SQL Conn calls", b.connCalls)
	}
}

func TestRedisTreeDBOrder(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{Database: "4"})
	r.fill(3, "a", 2)
	r.fill(7, "b", 1)
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	var names []string
	for _, c := range m.tree.Roots()[0].Children {
		names = append(names, c.Name)
	}
	if len(names) != 16 || names[0] != "db3" || names[1] != "db7" || names[2] != "db0" {
		t.Fatalf("dbs = %v", names)
	}
}

func TestRedisTreeLoadMore(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.fill(0, "k", 25_000)
	dbn := expandDB0(t, m)
	if got := countKeys(dbn); got != 10_000 {
		t.Fatalf("first load = %d keys", got)
	}
	more := moreNode(dbn)
	if more == nil || !strings.Contains(more.Name, "load more") || !strings.Contains(more.Name, "25,000") {
		t.Fatalf("more = %+v", more)
	}
	m.tree.SelectWhere(func(n *tree.Node) bool { return n.Kind == tree.KindMore })
	pressAll(t, m, "enter")
	if got := countKeys(dbn); got != 20_000 {
		t.Fatalf("after load more = %d keys", got)
	}
	if scans := r.scanCalls(); scans[10].cursor != 10_000 {
		t.Fatalf("load more restarted the scan: %+v", scans[10])
	}
}

// startScan expands db0 with r blocked, so the first batch is in flight.
func startBlockedScan(t *testing.T, m *Model, r *fakeRedis) tea.Cmd {
	t.Helper()
	m.setFocus(focusTree)
	pressAll(t, m, "l", "j")
	r.mu.Lock()
	r.block = make(chan struct{})
	r.mu.Unlock()
	_, cmd := m.Update(uitest.Key("l"))
	return cmd
}

func TestRedisScanStaleBatch(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.fill(0, "k", 50)
	old := startBlockedScan(t, m, r)
	dbn := m.tree.Selected()
	r.mu.Lock()
	close(r.block)
	r.block = nil
	r.mu.Unlock()
	stale := old() // the first scan's batch, not yet delivered
	r.fill(0, "k", 80)
	_, cmd := m.Update(uitest.Key("space"))
	drainAll(t, m, cmd)
	_, cmd = m.Update(uitest.Key("r"))
	drainAll(t, m, cmd)
	if got := countKeys(dbn); got != 80 {
		t.Fatalf("after refresh = %d keys", got)
	}
	r.fill(0, "z", 5) // a stale batch must not add these either way
	_, cmd = m.Update(stale)
	drainAll(t, m, cmd)
	if got := countKeys(dbn); got != 80 {
		t.Fatalf("stale batch changed the tree: %d keys", got)
	}
}

func TestRedisScanCancel(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.fill(0, "k", 50)
	inflight := startBlockedScan(t, m, r)
	dbn := m.tree.Selected()
	pressAll(t, m, "ctrl+c")
	if !strings.Contains(m.status, "scan stopped") || dbn.Loading {
		t.Fatalf("status = %q, loading %v", m.status, dbn.Loading)
	}
	drainAll(t, m, inflight) // the canceled batch returns its error
	if got := countKeys(dbn); got != 0 || m.statusErr {
		t.Fatalf("canceled batch: keys %d, status %q", got, m.status)
	}
}

func TestRedisSQLActionsRefused(t *testing.T) {
	m, b, _ := newRedisModel(t, config.Connection{})
	m.setFocus(focusTree)
	for _, keys := range [][]string{{"C"}, {"m"}, {"space", "s"}, {"I"}, {"T"}, {"r"}} {
		pressAll(t, m, keys...)
		if m.status != "not supported for redis yet" {
			t.Fatalf("%v: status = %q", keys, m.status)
		}
	}
	pressAll(t, m, "l")
	if b.connCalls != 0 || len(m.tabs) != 0 {
		t.Fatalf("Conn calls = %d, tabs = %d", b.connCalls, len(m.tabs))
	}
	if _, ok := metaKeyFor(b, "cache", ""); ok {
		t.Fatal("redis has no catalog")
	}
}

func TestRedisPasswordPrompt(t *testing.T) {
	m, b, r := newRedisModel(t, config.Connection{})
	r.fill(0, "k", 3)
	b.needPassword = true
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	if _, ok := m.overlay.(*overlay.Password); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, "pw")
	pressAll(t, m, "enter")
	if root := m.tree.Roots()[0]; !root.Loaded || len(root.Children) != 16 {
		t.Fatalf("root not reloaded: %+v", root)
	}
}

// lastScan is the last SCAN fakeRedis received.
func lastScan(t *testing.T, r *fakeRedis) fakeScanCall {
	t.Helper()
	scans := r.scanCalls()
	if len(scans) == 0 {
		t.Fatal("no scan")
	}
	return scans[len(scans)-1]
}

func TestRedisFilter(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.fill(0, "user:", 3)
	r.fill(0, "sess:", 2)
	dbn := expandDB0(t, m)
	pressAll(t, m, "F")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, "sess")
	pressAll(t, m, "enter")
	if got := lastScan(t, r).match; got != "*sess*" {
		t.Fatalf("match = %q", got)
	}
	if got := countKeys(dbn); got != 2 || !strings.Contains(dbn.Detail, "filter *sess*") {
		t.Fatalf("keys = %d, detail %q", got, dbn.Detail)
	}
	pressAll(t, m, "f", "x")
	if got := countKeys(dbn); got != 5 || lastScan(t, r).match != "" || strings.Contains(dbn.Detail, "filter") {
		t.Fatalf("after clear: keys = %d, match %q, detail %q", got, lastScan(t, r).match, dbn.Detail)
	}
}

func TestRedisFilterStopsOnScanned(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.fill(0, "k", 150_000)
	expandDB0(t, m)
	before := len(r.scanCalls())
	pressAll(t, m, "F")
	typeText(t, m, "nomatch")
	pressAll(t, m, "enter")
	if n := len(r.scanCalls()) - before; n != 100 {
		t.Fatalf("filter load sent %d scans", n)
	}
	more := moreNode(m.redisDBNode(redisDBKey{"cache", 0}))
	if more == nil || !strings.Contains(more.Name, "found 0") || !strings.Contains(more.Name, "load more") {
		t.Fatalf("more = %+v", more)
	}
}

func TestRedisFilterFromFolder(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.fill(0, "user:", 3)
	expandDB0(t, m)
	pressAll(t, m, "j") // the user folder
	if n := m.tree.Selected(); n.Kind != tree.KindKeyFolder {
		t.Fatalf("selected %+v", n)
	}
	pressAll(t, m, "F")
	if s := screen(m); !strings.Contains(s, "user:*") {
		t.Fatalf("prompt:\n%s", s)
	}
}

func TestRedisFilterIgnoredOnSQL(t *testing.T) {
	m, _ := newTestModel(t)
	m.setFocus(focusTree)
	pressAll(t, m, "F")
	if m.overlay != nil {
		t.Fatalf("overlay = %T", m.overlay)
	}
}

func TestOpenKeyNotFound(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.fill(0, "user:", 3)
	expandDB0(t, m)
	pressAll(t, m, "space", "k")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, "nope")
	pressAll(t, m, "enter")
	if m.status != "key not found: nope" || len(m.tabs) != 0 {
		t.Fatalf("status = %q, tabs %d", m.status, len(m.tabs))
	}
}

func TestOpenKeyCompletion(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.fill(0, "user:", 2)
	r.fill(0, "sess:", 1)
	expandDB0(t, m)
	cands := m.keyCandidates(redisDBKey{"cache", 0}, "user")
	if len(cands) != 2 || cands[0].Text != "user:0" || cands[1].Text != "user:1" {
		t.Fatalf("candidates = %+v", cands)
	}
}

func TestOpenKeyNoRedisContext(t *testing.T) {
	m, _ := newTestModel(t)
	m.setFocus(focusTree)
	pressAll(t, m, "space", "k")
	if m.status != "select a redis connection first" || m.overlay != nil {
		t.Fatalf("status = %q", m.status)
	}
}

func TestRedisCollapseDuringScan(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.fill(0, "k", 5000)
	m.setFocus(focusTree)
	pressAll(t, m, "l", "j")
	_, cmd := m.Update(uitest.Key("l")) // the first batch is in flight
	dbn := m.tree.Selected()
	pressAll(t, m, "h")
	drainAll(t, m, cmd)
	if dbn.Expanded {
		t.Fatal("a scan batch opened the db the user closed")
	}
	if got := countKeys(dbn); got != 5000 {
		t.Fatalf("keys = %d", got)
	}
}

func TestRefreshKeepsFolderSelection(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.fill(0, "user:", 3)
	r.put(0, "other", fakeValue{typ: "string", str: "v"})
	expandDB0(t, m)
	pressAll(t, m, "j")
	if n := m.tree.Selected(); n.Kind != tree.KindKeyFolder || n.Key != "user:" {
		t.Fatalf("selected %+v", n)
	}
	pressAll(t, m, "space", "r")
	if n := m.tree.Selected(); n.Kind != tree.KindKeyFolder || n.Key != "user:" {
		t.Fatalf("after refresh the cursor is on %v %q, want the folder", n.Kind, n.Key)
	}
}
