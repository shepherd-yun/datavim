package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/rcmd"
	"github.com/shepherd-yun/datavim/internal/rdb"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

const (
	bigKeysTop   = 20   // the biggest keys kept per type
	bigKeysBatch = 1000 // SCAN COUNT of a big-key analysis
)

// typeOrder is the order types are listed in; other types follow by name.
var typeOrder = []string{"string", "hash", "list", "set", "zset", "stream"}

// bigKeyStats ranks the biggest keys of each type. Pure.
type bigKeyStats struct {
	top     int
	byType  map[string]*typeStats
	memSeen bool // some key had a MEMORY USAGE
}

type typeStats struct {
	count, total int64         // keys seen and the sum of their lengths (SCAN may repeat a key: approximate)
	biggest      []rdb.KeySize // at most top, by Len desc, then Key; one entry per key
}

func newBigKeyStats(top int) *bigKeyStats {
	return &bigKeyStats{top: top, byType: map[string]*typeStats{}}
}

func (s *bigKeyStats) add(sizes []rdb.KeySize) {
	for _, k := range sizes {
		ts := s.byType[k.Type]
		if ts == nil {
			ts = &typeStats{}
			s.byType[k.Type] = ts
		}
		ts.count++
		if k.Mem >= 0 {
			s.memSeen = true
		}
		if k.Len < 0 {
			continue // module types are only counted
		}
		ts.total += k.Len
		ts.biggest = slices.DeleteFunc(ts.biggest, func(o rdb.KeySize) bool { return o.Key == k.Key })
		ts.biggest = append(ts.biggest, k)
		sort.SliceStable(ts.biggest, func(i, j int) bool {
			a, b := ts.biggest[i], ts.biggest[j]
			if a.Len != b.Len {
				return a.Len > b.Len
			}
			return a.Key < b.Key
		})
		if len(ts.biggest) > s.top {
			ts.biggest = ts.biggest[:s.top]
		}
	}
}

// types are the types seen, in typeOrder and then by name.
func (s *bigKeyStats) types() []string {
	var out, rest []string
	for _, t := range typeOrder {
		if s.byType[t] != nil {
			out = append(out, t)
		}
	}
	for t := range s.byType {
		if !slices.Contains(typeOrder, t) {
			rest = append(rest, t)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// sizeText is a key's length: bytes for a string, else "N fields" etc.
func sizeText(typ string, n int64) string {
	if typ == "string" {
		return humanBytes(n)
	}
	return groupDigits(n) + " " + itemUnit(typ)
}

// rows are the ranked keys, raw, and the grid showing them (keys escaped).
func (s *bigKeyStats) rows() ([]rdb.KeySize, *db.ResultSet) {
	var keys []rdb.KeySize
	var rows [][]string
	for _, t := range s.types() {
		for _, k := range s.byType[t].biggest {
			mem := ""
			if k.Mem >= 0 {
				mem = humanBytes(k.Mem)
			}
			keys = append(keys, k)
			rows = append(rows, []string{k.Type, rcmd.Escape(k.Key), sizeText(k.Type, k.Len), mem})
		}
	}
	return keys, textResult([]string{"type", "key", "length", "memory"}, rows)
}

// summary is a line per type: "hash  1,234 keys · 5,678,901 fields ·
// biggest user:1 (12,345 fields)".
func (s *bigKeyStats) summary() []string {
	var out []string
	for _, t := range s.types() {
		ts := s.byType[t]
		line := t + "  " + plural64(ts.count, "key")
		if len(ts.biggest) > 0 {
			b := ts.biggest[0]
			line += " · " + sizeText(t, ts.total) + " · biggest " + rcmd.Escape(b.Key) + " (" + sizeText(t, b.Len) + ")"
		}
		out = append(out, line)
	}
	return out
}

type bigKeysPhase int

const (
	bigRunning bigKeysPhase = iota
	bigDone
	bigStopped
	bigFailed
)

func bigKeysTabKey(conn string, n int) string { return "rbig\x00" + conn + "\x00" + strconv.Itoa(n) }

type (
	// bigKeysChosenMsg is the options picked for an analysis.
	bigKeysChosenMsg struct {
		key     redisDBKey
		pattern string
		memory  bool
	}
	// bigKeysRunMsg starts (or, with restart, restarts) an analysis, confirmed on prod.
	bigKeysRunMsg struct {
		bigKeysChosenMsg
		restart bool
	}
)

// bigKeysStepMsg is one batch: the sizes of the keys one SCAN returned.
type bigKeysStepMsg struct {
	tabID, gen int
	conn       string
	sizes      []rdb.KeySize
	cursor     uint64
	scanned    int64
	total      int64 // DBSIZE, with the first batch; -1 otherwise
	err        error
}

func (m bigKeysStepMsg) TabID() int               { return m.tabID }
func (m bigKeysStepMsg) failure() (string, error) { return m.conn, m.err }

// startBigKeys is B on a redis node: pick the options.
func (m *Model) startBigKeys() tea.Cmd {
	n := m.tree.Selected()
	var k redisDBKey
	ok := false
	if n != nil {
		k, ok = redisKeyOf(n)
	}
	if !ok {
		m.setStatus("select a Redis db to analyze", true)
		return nil
	}
	filter := ""
	if s := m.scans[k]; s != nil {
		filter = s.pattern
	}
	pattern := bulkPattern(n, filter)
	title := fmt.Sprintf("Analyze big keys in %s/db%d (%s)", k.conn, k.db, rcmd.Escape(pattern))
	m.overlay = overlay.NewChoice(title, []string{"Lengths only", "Lengths and memory (MEMORY USAGE, slower)"}, func(i int) tea.Msg {
		return bigKeysChosenMsg{key: k, pattern: pattern, memory: i == 1}
	})
	return nil
}

// bigKeysConfirm asks before an analysis of a prod db.
func bigKeysConfirm(msg bigKeysRunMsg) overlay.Overlay {
	return overlay.NewConfirm("Analyze big keys", []string{
		fmt.Sprintf("Scan every key matching %s in PROD %s/db%d?", rcmd.Escape(msg.pattern), msg.key.conn, msg.key.db),
		"It reads in batches of 1,000 with a pause between them.",
	}, "", msg)
}

func (m *Model) bigKeysChosen(msg bigKeysChosenMsg) tea.Cmd {
	run := bigKeysRunMsg{bigKeysChosenMsg: msg}
	if cfg, _ := m.backend.Connection(msg.key.conn); cfg.Env == config.EnvProd {
		m.overlay = bigKeysConfirm(run)
		return nil
	}
	return m.runBigKeys(run)
}

// runBigKeys opens the analysis tab, or switches to it: a running one goes
// on unless restart, a finished one starts over with the new options.
func (m *Model) runBigKeys(msg bigKeysRunMsg) tea.Cmd {
	key := bigKeysTabKey(msg.key.conn, msg.key.db)
	for i, t := range m.tabs {
		if bt, ok := t.(*bigKeysTab); ok && t.Key() == key {
			m.gotoTab(i)
			if bt.phase == bigRunning && !msg.restart {
				return nil
			}
			bt.pattern, bt.memory = msg.pattern, msg.memory
			return bt.start()
		}
	}
	return m.showTab(key, func(id int) tab {
		return newBigKeysTab(id, m.backend, m.keys, msg.key, msg.pattern, msg.memory, m.settings)
	})
}

// bigKeysTab is a big-key analysis of one db: SCAN in batches, the
// lengths of each batch in one pipeline, a pause between batches.
type bigKeysTab struct {
	id      int
	backend Backend
	keys    *keymap.Keymap
	conn    string
	db      int
	pattern string
	memory  bool

	stats   *bigKeyStats
	ranked  []rdb.KeySize // the grid's rows, raw
	phase   bigKeysPhase
	cursor  uint64
	scanned int64
	total   int64
	started time.Time
	took    time.Duration
	err     string
	gen     int
	ctx     context.Context
	cancel  context.CancelFunc

	grid          *grid.Model
	width, height int
}

func newBigKeysTab(id int, b Backend, keys *keymap.Keymap, k redisDBKey, pattern string, memory bool, s config.Settings) *bigKeysTab {
	return &bigKeysTab{
		id: id, backend: b, keys: keys, conn: k.conn, db: k.db, pattern: pattern, memory: memory,
		stats: newBigKeyStats(bigKeysTop), grid: grid.New(nil, s.MaxColumnWidth),
	}
}

func (t *bigKeysTab) ID() int                     { return t.id }
func (t *bigKeysTab) Title() string               { return fmt.Sprintf("%s/db%d · big keys", t.conn, t.db) }
func (t *bigKeysTab) Key() string                 { return bigKeysTabKey(t.conn, t.db) }
func (t *bigKeysTab) Context() keymap.Context     { return keymap.RedisBigKeys }
func (t *bigKeysTab) Init() tea.Cmd               { return t.start() }
func (t *bigKeysTab) SetSize(w, h int)            { t.width, t.height = max(w, 0), max(h, 0) }
func (t *bigKeysTab) activeGrid() *grid.Model     { return t.grid }
func (t *bigKeysTab) retryAfterPassword() tea.Cmd { return t.start() }

func (t *bigKeysTab) Target() (string, db.TableRef) {
	return t.conn, db.TableRef{Namespace: db.Namespace{Database: strconv.Itoa(t.db)}}
}

func (t *bigKeysTab) Find(q string, forward bool) bool { return t.grid.Find(q, forward) }

func (t *bigKeysTab) exportSource() exportSrc {
	return exportSrc{rs: t.grid.Result(), name: "bigkeys", conn: t.conn}
}

// start begins the analysis afresh, stopping a running one.
func (t *bigKeysTab) stop() {
	t.gen++
	if t.cancel != nil {
		t.cancel()
	}
}

func (t *bigKeysTab) start() tea.Cmd {
	t.stop()
	t.ctx, t.cancel = context.WithCancel(context.Background())
	t.stats, t.ranked = newBigKeyStats(bigKeysTop), nil
	t.phase, t.cursor, t.scanned, t.total, t.err = bigRunning, 0, 0, -1, ""
	t.started = time.Now()
	t.grid.SetResult(nil)
	return t.step(false)
}

// step reads the next batch, after a pause unless it is the first.
func (t *bigKeysTab) step(pause bool) tea.Cmd {
	id, gen, ctx, b, conn, n := t.id, t.gen, t.ctx, t.backend, t.conn, t.db
	cursor, pattern, memory, withTotal := t.cursor, t.pattern, t.memory, t.total < 0
	return func() tea.Msg {
		msg := bigKeysStepMsg{tabID: id, gen: gen, conn: conn, total: -1}
		if pause {
			select {
			case <-ctx.Done():
				msg.err = ctx.Err()
				return msg
			case <-time.After(prodScanPause):
			}
		}
		msg.err = withRedis(ctx, b, conn, n, func(c rdb.Client) error {
			if withTotal {
				total, err := c.DBSize(ctx)
				if err != nil {
					return err
				}
				msg.total = total
			}
			page, err := c.Scan(ctx, cursor, pattern, bigKeysBatch)
			if err != nil {
				return err
			}
			msg.cursor, msg.scanned = page.Cursor, int64(len(page.Keys))
			msg.sizes, err = c.KeySizes(ctx, page.Keys, memory)
			return err
		})
		return msg
	}
}

func (t *bigKeysTab) Update(msg tea.Msg) tea.Cmd {
	m, ok := msg.(bigKeysStepMsg)
	if !ok || m.gen != t.gen || t.phase != bigRunning {
		return nil
	}
	if m.err != nil {
		if errors.Is(m.err, context.Canceled) {
			return nil
		}
		t.phase, t.err, t.took = bigFailed, serverErr(m.err), time.Since(t.started)
		return nil
	}
	if m.total >= 0 {
		t.total = m.total
	}
	t.stats.add(m.sizes)
	t.scanned += m.scanned
	t.cursor = m.cursor
	var rs *db.ResultSet
	t.ranked, rs = t.stats.rows()
	t.grid.ReplaceResult(rs)
	if t.cursor == 0 {
		t.phase, t.took = bigDone, time.Since(t.started)
		t.cancel()
		return nil
	}
	return t.step(true)
}

// cancelRun is Cancel: stop, keeping what was found.
func (t *bigKeysTab) cancelRun() tea.Cmd {
	if t.phase == bigRunning {
		t.stop()
		t.phase = bigStopped
	}
	return nil
}

// shutdown stops the scan when the tab closes.
func (t *bigKeysTab) shutdown() func() {
	t.stop()
	return func() {}
}

func (t *bigKeysTab) Handle(a keymap.Action) tea.Cmd {
	switch a {
	case keymap.Refresh:
		if t.phase == bigRunning {
			t.cancelRun()
		}
		if cfg, _ := t.backend.Connection(t.conn); cfg.Env == config.EnvProd {
			run := bigKeysRunMsg{bigKeysChosenMsg: bigKeysChosenMsg{key: redisDBKey{conn: t.conn, db: t.db}, pattern: t.pattern, memory: t.memory}, restart: true}
			return openOverlay(bigKeysConfirm(run))
		}
		return t.start()
	case keymap.Open:
		r, _ := t.grid.Position()
		if r >= len(t.ranked) {
			return nil
		}
		k := openKeyMsg{conn: t.conn, db: t.db, key: t.ranked[r].Key}
		return func() tea.Msg { return k }
	}
	if cmd, ok := gridAction(t.grid, a); ok {
		return cmd
	}
	return nil
}

func (t *bigKeysTab) header() string {
	p := rcmd.Escape(t.pattern)
	switch t.phase {
	case bigRunning:
		of := groupDigits(t.scanned)
		if t.total >= 0 {
			of += " / " + groupDigits(t.total)
		}
		return joinHints(fmt.Sprintf("scanning %s · %s keys · %ds", p, of, int(time.Since(t.started).Seconds())),
			keyHint(t.keys, keymap.Global, keymap.Cancel, "stops"))
	case bigDone:
		return fmt.Sprintf("done · scanned %s keys in %.1fs", groupDigits(t.scanned), t.took.Seconds())
	case bigStopped:
		return fmt.Sprintf("stopped · scanned %s of %s keys", groupDigits(t.scanned), groupDigits(max(t.total, t.scanned)))
	}
	return fmt.Sprintf("failed · scanned %s keys", groupDigits(t.scanned))
}

func (t *bigKeysTab) View(focused bool) string {
	lines := []string{t.header()}
	if t.err != "" {
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.err)))
	}
	sum := t.stats.summary()
	if t.memory && len(sum) > 0 && !t.stats.memSeen {
		sum = append(sum, "MEMORY USAGE not allowed")
	}
	for _, s := range sum {
		lines = append(lines, theme.Muted.Render(textutil.Sanitize(s)))
	}
	for i, l := range lines {
		lines[i] = textutil.Fit(l, t.width)
	}
	if t.grid.Result() == nil || t.grid.RowCount() == 0 {
		return strings.Join(lines, "\n")
	}
	t.grid.SetSize(t.width, max(t.height-len(lines), 0))
	return strings.Join(lines, "\n") + "\n" + t.grid.View(focused)
}
