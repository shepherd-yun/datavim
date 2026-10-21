package ui

import (
	"context"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/rcmd"
	"github.com/shepherd-yun/datavim/internal/rdb"
	"github.com/shepherd-yun/datavim/internal/ui/panes/complete"
)

// The background SCAN that adds keys to a completion: at most
// completeScanBatches batches of completeScanCount, stopping once
// completeScanKeys keys are found.
const (
	completeScanBatches = 5
	completeScanCount   = 1000
	completeScanKeys    = 50
	// fewKeys is when the tree's keys are too few and a scan is worth it.
	fewKeys = 10
)

// redisCompleteScanMsg is a completion SCAN's keys; complete means the
// scan walked the whole db, so longer prefixes need no scan of their own.
type redisCompleteScanMsg struct {
	tabID, gen int
	prefix     string
	keys       []string
	complete   bool
}

func (m redisCompleteScanMsg) TabID() int { return m.tabID }

// redisKeys returns the keys of (conn, db) the tree has scanned that start
// with prefix, sorted, at most limit. UI goroutine only.
func (m *Model) redisKeys(conn string, db int, prefix string, limit int) []string {
	s := m.scans[redisDBKey{conn: conn, db: db}]
	if s == nil {
		return nil
	}
	var out []string
	for k := range s.keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// popupKey is the completion popup's keys in INSERT mode, like the SQL
// console's.
func (t *redisConsoleTab) popupKey(key string) (cmd tea.Cmd, used bool) {
	if t.popup == nil {
		if key == "ctrl+n" || key == "ctrl+p" {
			return t.suggest(true), true
		}
		return nil, false
	}
	switch key {
	case "down", "ctrl+n", "ctrl+j", "tab":
		t.popup.Next()
	case "up", "ctrl+p", "ctrl+k", "shift+tab":
		t.popup.Prev()
	case "enter", "ctrl+y":
		c := t.popup.Chosen()
		t.popup = nil
		t.editor.ReplaceBeforeCursor(t.popStart, c.Text)
		return tea.Batch(t.suggest(false), t.scheduleDraft()), true
	case "esc": // closes the popup; a second esc leaves INSERT mode
		t.popup = nil
	default:
		return nil, false
	}
	return nil, true
}

// completion is the completion context at the cursor's line.
func (t *redisConsoleTab) completion() rcmd.Completion {
	text, off := t.editor.Value(), t.editor.Offset()
	start := strings.LastIndexByte(text[:off], '\n') + 1
	end := len(text)
	if i := strings.IndexByte(text[off:], '\n'); i >= 0 {
		end = off + i
	}
	c := rcmd.CompleteAt(text[start:end], off-start)
	c.Start += start
	return c
}

// suggest updates the argument hint and opens (or closes) the popup for
// the word at the cursor; without force a command name needs a letter
// first. It may start a background SCAN for more keys.
func (t *redisConsoleTab) suggest(force bool) tea.Cmd {
	t.popup = nil
	c := t.completion()
	t.hint = c.Hint
	var cands []complete.Candidate
	var scan tea.Cmd
	switch c.Kind {
	case rcmd.CompNone:
		return nil
	case rcmd.CompCommand:
		if c.Prefix == "" && !force {
			return nil
		}
		for _, n := range rcmd.Names() {
			cmd, _ := rcmd.Lookup([]string{n})
			cands = append(cands, complete.Candidate{Text: n + " ", Label: n, Kind: complete.Command, Detail: cmd.Args})
		}
	case rcmd.CompSubcommand:
		for _, n := range rcmd.SubNames(c.Command.Name) {
			sub, _ := rcmd.Lookup([]string{c.Command.Name, n})
			cands = append(cands, complete.Candidate{Text: n + " ", Label: n, Kind: complete.Command, Detail: sub.Args})
		}
	case rcmd.CompKey:
		keys := t.keyCandidates(c.Prefix)
		if len(keys) < fewKeys && c.Prefix != "" {
			scan = t.scanForKeys(c.Prefix)
		}
		if c.Prefix == "" && !force {
			return scan
		}
		for _, k := range keys {
			cands = append(cands, complete.Candidate{Text: rcmd.Quote(k), Label: rcmd.Escape(k), Kind: complete.Key})
		}
	}
	ranked := complete.Rank(c.Prefix, cands, maxCandidates)
	if c.Kind == rcmd.CompKey {
		ranked = cands[:min(len(cands), maxCandidates)] // keys already start with the prefix
	}
	if len(ranked) == 0 || !force && len(ranked) == 1 && strings.EqualFold(ranked[0].Label, rcmd.Escape(c.Prefix)) {
		return scan
	}
	t.popup, t.popStart = complete.New(ranked), c.Start
	return scan
}

// keyCandidates is the tree's keys of the current db with prefix, and the
// ones the console's own scans found, sorted.
func (t *redisConsoleTab) keyCandidates(prefix string) []string {
	var keys []string
	if t.keysFn != nil {
		keys = t.keysFn(t.db, prefix)
	}
	for k := range t.extraKeys {
		if strings.HasPrefix(k, prefix) && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys[:min(len(keys), completeScanKeys)]
}

// scanForKeys starts a SCAN MATCH prefix* unless one already covered it.
func (t *redisConsoleTab) scanForKeys(prefix string) tea.Cmd {
	for p, complete := range t.scanned {
		if p == prefix || complete && strings.HasPrefix(prefix, p) {
			return nil
		}
	}
	t.scanned[prefix] = false
	id, gen, b, conn, n := t.id, t.keyGen, t.backend, t.conn, t.db
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := redisCompleteScanMsg{tabID: id, gen: gen, prefix: prefix}
		match := rcmd.GlobEscape(prefix) + "*"
		_ = withRedis(ctx, b, conn, n, func(c rdb.Client) error {
			var cursor uint64
			for range completeScanBatches {
				page, err := c.Scan(ctx, cursor, match, completeScanCount)
				if err != nil {
					return err
				}
				for _, k := range page.Keys {
					msg.keys = append(msg.keys, k.Key)
				}
				cursor = page.Cursor
				if cursor == 0 {
					msg.complete = true
					return nil
				}
				if len(msg.keys) >= completeScanKeys {
					return nil
				}
			}
			return nil
		})
		return msg
	}
}

// keysScanned takes a completion SCAN's keys and refreshes the popup.
func (t *redisConsoleTab) keysScanned(m redisCompleteScanMsg) tea.Cmd {
	if m.gen != t.keyGen {
		return nil
	}
	for _, k := range m.keys {
		t.extraKeys[k] = true
	}
	t.scanned[m.prefix] = m.complete
	if !t.editor.Insert() || t.onResults {
		return nil
	}
	if c := t.completion(); c.Kind == rcmd.CompKey && strings.HasPrefix(c.Prefix, m.prefix) {
		return t.suggest(false)
	}
	return nil
}

// forgetKeys drops the console's scanned keys when the db changes.
func (t *redisConsoleTab) forgetKeys() {
	t.keyGen++
	t.extraKeys, t.scanned = map[string]bool{}, map[string]bool{}
}
