package ui

import (
	"context"
	"errors"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/rcmd"
	"github.com/shepherd-yun/datavim/internal/rdb"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

const (
	bulkPreview = 20   // keys the confirmation lists
	bulkUnlink  = 500  // keys per UNLINK
	bulkScan    = 1000 // SCAN COUNT of a bulk delete's batches
)

type bulkPhase int

const (
	bulkCounting bulkPhase = iota
	bulkConfirming
	bulkDeleting
)

// bulkDelete is the one running pattern delete (one at a time per Model,
// design §6.3).
type bulkDelete struct {
	key     redisDBKey
	pattern string
	phase   bulkPhase
	cursor  uint64
	scanned int64
	matched int64    // counting: keys that matched
	sample  []string // the first bulkPreview matches, raw
	deleted int64
	total   int64 // DBSIZE when it started
	gen     int
	ctx     context.Context
	cancel  context.CancelFunc
}

// bulkStepMsg is one batch of a bulk delete: the keys it matched
// (counting) or how many it deleted.
type bulkStepMsg struct {
	gen     int
	conn    string
	keys    []string
	cursor  uint64
	scanned int64
	deleted int64
	total   int64 // DBSIZE, with the first batch; -1 otherwise
	err     error
}

func (m bulkStepMsg) failure() (string, error) { return m.conn, m.err }

// bulkConfirmedMsg is the confirmation of a counted bulk delete.
type bulkConfirmedMsg struct{ gen int }

// bulkPattern is the SCAN MATCH pattern a bulk delete on n uses: a
// folder's keys, or a db's filtered keys (every key without a filter).
func bulkPattern(n *tree.Node, filter string) string {
	if n.Kind == tree.KindKeyFolder {
		return rcmd.GlobEscape(n.Key) + "*"
	}
	if filter != "" {
		return filter
	}
	return "*"
}

// startBulkDelete is dd on a db or folder: count, confirm, delete.
func (m *Model) startBulkDelete(n *tree.Node) tea.Cmd {
	k, _ := redisKeyOf(n)
	if cfg, _ := m.backend.Connection(k.conn); cfg.IsReadOnly() {
		m.setStatus(readOnlyHint, true)
		return nil
	}
	if m.bulk != nil && m.bulk.phase != bulkConfirming {
		m.setStatus(joinHints("a bulk delete is running", keyHint(m.keys, keymap.Global, keymap.Cancel, "stops it")), true)
		return nil
	}
	if !m.canChange(k.conn) {
		return nil
	}
	filter := ""
	if s := m.scans[k]; s != nil {
		filter = s.pattern
	}
	m.bulkGen++
	ctx, cancel := context.WithCancel(context.Background())
	m.bulk = &bulkDelete{key: k, pattern: bulkPattern(n, filter), total: -1, gen: m.bulkGen, ctx: ctx, cancel: cancel}
	m.bulkStatus()
	return m.bulkStep(false)
}

func (m *Model) bulkStatus() {
	b := m.bulk
	p := rcmd.Escape(b.pattern)
	switch b.phase {
	case bulkCounting:
		scanned := groupDigits(b.scanned)
		if b.total >= 0 {
			scanned = groupDigits(min(b.scanned, b.total)) + " / " + groupDigits(b.total)
		}
		m.setStatus("counting keys matching "+p+" · scanned "+scanned+" · "+groupDigits(b.matched)+" found", false)
	case bulkDeleting:
		m.setStatus("deleting keys matching "+p+" · "+groupDigits(b.deleted)+" deleted", false)
	}
}

// bulkStep reads (and when deleting, unlinks) the next batch; on prod it
// waits a moment first.
func (m *Model) bulkStep(pause bool) tea.Cmd {
	bd := m.bulk
	b, k, gen, ctx, cursor, pattern, phase := m.backend, bd.key, bd.gen, bd.ctx, bd.cursor, bd.pattern, bd.phase
	withTotal := phase == bulkCounting && cursor == 0
	if cfg, _ := b.Connection(k.conn); cfg.Env != config.EnvProd {
		pause = false
	}
	return func() tea.Msg {
		if pause {
			select {
			case <-time.After(prodScanPause):
			case <-ctx.Done():
				return bulkStepMsg{gen: gen, conn: k.conn, err: ctx.Err()}
			}
		}
		msg := bulkStepMsg{gen: gen, conn: k.conn, total: -1, scanned: bulkScan}
		if err := ctx.Err(); err != nil {
			msg.err = err // stopped before this batch began
			return msg
		}
		msg.err = withRedis(ctx, b, k.conn, k.db, func(c rdb.Client) error {
			if withTotal {
				n, err := c.DBSize(ctx)
				if err != nil {
					return err
				}
				msg.total = n
			}
			keys, next, err := c.ScanKeys(ctx, cursor, pattern, bulkScan)
			if err != nil {
				return err
			}
			msg.cursor = next
			if phase == bulkCounting {
				msg.keys = keys
				return nil
			}
			for i := 0; i < len(keys); i += bulkUnlink {
				if err := ctx.Err(); err != nil {
					return err
				}
				n, err := c.Unlink(ctx, keys[i:min(i+bulkUnlink, len(keys))]...)
				msg.deleted += n
				if err != nil {
					return err
				}
			}
			return nil
		})
		return msg
	}
}

// bulkStepped takes one batch: the next one, the confirmation, or the end.
func (m *Model) bulkStepped(msg bulkStepMsg) tea.Cmd {
	b := m.bulk
	if b == nil || msg.gen != b.gen {
		return nil // stopped meanwhile
	}
	b.deleted += msg.deleted
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return nil
		}
		err := msg.err.Error()
		if b.phase == bulkDeleting {
			err = joinHints(err, "deleted "+plural64(b.deleted, "key"))
		}
		return m.endBulk(err, true)
	}
	if msg.total >= 0 {
		b.total = msg.total
	}
	b.cursor = msg.cursor
	b.scanned += msg.scanned
	if b.phase == bulkCounting {
		b.matched += int64(len(msg.keys))
		for _, k := range msg.keys {
			if len(b.sample) < bulkPreview {
				b.sample = append(b.sample, k)
			}
		}
		if b.cursor != 0 {
			m.bulkStatus()
			return m.bulkStep(true)
		}
		if b.matched == 0 {
			b.cancel()
			m.bulk = nil
			m.setStatus("no keys match "+rcmd.Escape(b.pattern), false)
			return nil
		}
		m.confirmBulk()
		return nil
	}
	if b.cursor != 0 {
		m.bulkStatus()
		return m.bulkStep(true)
	}
	return m.endBulk("deleted "+plural64(b.deleted, "key"), false)
}

func plural64(n int64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return groupDigits(n) + " " + noun + "s"
}

// confirmBulk shows the count and the first keys: y, or the connection's
// name on prod.
func (m *Model) confirmBulk() {
	b := m.bulk
	b.phase = bulkConfirming
	m.setStatus("", false)
	lines := []string{"Delete " + plural64(b.matched, "key") + " matching " + rcmd.Escape(b.pattern) +
		" in " + b.key.conn + "/db" + strconv.Itoa(b.key.db) + "?"}
	for _, k := range b.sample {
		lines = append(lines, "  "+rcmd.Escape(k))
	}
	if more := b.matched - int64(len(b.sample)); more > 0 {
		lines = append(lines, "  … and "+groupDigits(more)+" more")
	}
	lines = append(lines, "Keys written while deleting may be missed.", "This cannot be undone.")
	expect := ""
	if m.isProd(b.key.conn) {
		expect = b.key.conn
	}
	m.overlay = overlay.NewConfirm("Delete keys", lines, expect, bulkConfirmedMsg{gen: b.gen})
}

func (m *Model) bulkConfirmed(msg bulkConfirmedMsg) tea.Cmd {
	b := m.bulk
	if b == nil || msg.gen != b.gen || b.phase != bulkConfirming {
		return nil
	}
	b.phase, b.cursor, b.scanned = bulkDeleting, 0, 0
	m.bulkStatus()
	return m.bulkStep(false)
}

// endBulk finishes the bulk delete with status s and scans its db again.
func (m *Model) endBulk(s string, isErr bool) tea.Cmd {
	b := m.bulk
	b.cancel()
	m.bulk = nil
	m.setStatus(s, isErr)
	if n := m.redisDBNode(b.key); n != nil {
		return m.refreshKeys(n)
	}
	return nil
}

// stopBulk is Cancel while a bulk delete counts or deletes.
func (m *Model) stopBulk() (tea.Cmd, bool) {
	b := m.bulk
	if b == nil || b.phase == bulkConfirming {
		return nil, false
	}
	m.bulkGen++
	if b.phase == bulkCounting {
		b.cancel()
		m.bulk = nil
		m.setStatus("stopped", false)
		return nil, true
	}
	return m.endBulk("stopped · deleted "+plural64(b.deleted, "key"), false), true
}
