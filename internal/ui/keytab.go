package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/rcmd"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/textview"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// maxStringRead is how much of a string the detail tab reads (design §4.3).
const maxStringRead = 1 << 20

// valueFormat is how a string value is shown.
type valueFormat int

const (
	fmtAuto valueFormat = iota
	fmtText
	fmtJSON
	fmtHex
)

func (f valueFormat) String() string {
	return [...]string{"auto", "text", "json", "hex"}[f]
}

// renderValue turns v into display lines. Auto shows a JSON object or
// array indented, other UTF-8 as text and anything else as hex; a forced
// JSON that is not JSON falls back to text. used is the format applied.
func renderValue(v string, f valueFormat) (lines []string, used valueFormat) {
	if f == fmtAuto {
		switch {
		case !utf8.ValidString(v) || hasControl(v):
			f = fmtHex
		case isJSON(v):
			f = fmtJSON
		default:
			f = fmtText
		}
	}
	switch f {
	case fmtJSON:
		if !isJSON(v) {
			return strings.Split(v, "\n"), fmtText
		}
		return strings.Split(textutil.PrettyJSON(v), "\n"), fmtJSON
	case fmtHex:
		return hexDump(v), fmtHex
	}
	return strings.Split(v, "\n"), fmtText
}

// hasControl reports whether v has control characters other than tab,
// newline and carriage return: such a value is binary, not text.
func hasControl(v string) bool {
	return strings.ContainsFunc(v, func(r rune) bool {
		return r != '\t' && r != '\n' && r != '\r' && unicode.IsControl(r)
	})
}

// isJSON reports whether v is a JSON object or array.
func isJSON(v string) bool {
	t := strings.TrimSpace(v)
	return (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t))
}

// hexDump is 16 bytes a line: offset, hex bytes, printable ASCII.
func hexDump(v string) []string {
	var out []string
	for off := 0; off < len(v); off += 16 {
		chunk := v[off:min(off+16, len(v))]
		var hx, asc strings.Builder
		for i := range len(chunk) {
			if i > 0 {
				hx.WriteByte(' ')
			}
			fmt.Fprintf(&hx, "%02x", chunk[i])
			if c := chunk[i]; c >= 0x20 && c < 0x7f {
				asc.WriteByte(c)
			} else {
				asc.WriteByte('.')
			}
		}
		out = append(out, fmt.Sprintf("%08x  %-47s  %s", off, hx.String(), asc.String()))
	}
	return out
}

func keyTabKey(conn string, n int, key string) string {
	return "rkey\x00" + conn + "\x00" + strconv.Itoa(n) + "\x00" + key
}

// keyLoadedMsg is a key's info with its value or a page of it.
type keyLoadedMsg struct {
	tabID     int
	conn      string
	info      rdb.KeyInfo // with the first page only
	first     bool
	text      string        // a string's value (what was read of it)
	rs        *db.ResultSet // raw cells: the tab escapes them for the grid
	next      uint64        // HSCAN / SSCAN cursor
	nextStart int64         // LRANGE / ZRANGE start
	nextAfter string        // stream ID
	more      bool
	err       error
}

func (m keyLoadedMsg) TabID() int               { return m.tabID }
func (m keyLoadedMsg) failure() (string, error) { return m.conn, m.err }

// keyFilterMsg is the pattern typed for a hash's fields or a set's members.
type keyFilterMsg struct {
	tabID int
	text  string
}

func (m keyFilterMsg) TabID() int { return m.tabID }

// keyTab shows one redis key, read only: a string in a text view, the
// other types in a grid loaded page by page.
type keyTab struct {
	id       int
	backend  Backend
	keys     *keymap.Keymap
	conn     string
	db       int
	key      string // raw
	pageSize int

	info    rdb.KeyInfo
	loaded  bool
	loading bool
	err     string
	status  string
	statusE bool

	text   string
	format valueFormat
	used   valueFormat
	view   *textview.Model

	grid       *grid.Model
	table      *edit.Table // the loaded rows, raw, with staged changes; nil for strings and module types
	submitting bool
	match      string
	next       uint64
	nextStart  int64
	nextAfter  string
	more       bool

	width, height int
}

func newKeyTab(id int, b Backend, keys *keymap.Keymap, conn string, n int, key string, s config.Settings) *keyTab {
	t := &keyTab{
		id: id, backend: b, keys: keys, conn: conn, db: n, key: key, pageSize: max(s.PageSize, 1),
		view: textview.New(), grid: grid.New(nil, s.MaxColumnWidth),
	}
	t.grid.SetMarks(t.mark)
	return t
}

// openKey opens (or shows) the detail tab of key in db n of conn.
func (m *Model) openKey(conn string, n int, key string) tea.Cmd {
	return m.showTab(keyTabKey(conn, n, key), func(id int) tab {
		return newKeyTab(id, m.backend, m.keys, conn, n, key, m.settings)
	})
}

func (t *keyTab) ID() int                 { return t.id }
func (t *keyTab) Title() string           { return rcmd.Escape(t.key) }
func (t *keyTab) Key() string             { return keyTabKey(t.conn, t.db, t.key) }
func (t *keyTab) Context() keymap.Context { return keymap.RedisKey }
func (t *keyTab) Init() tea.Cmd           { return t.load(true) }

func (t *keyTab) Target() (string, db.TableRef) {
	return t.conn, db.TableRef{Namespace: db.Namespace{Database: strconv.Itoa(t.db)}, Name: t.key}
}

func (t *keyTab) activeGrid() *grid.Model     { return t.grid }
func (t *keyTab) retryAfterPassword() tea.Cmd { return t.load(true) }
func (t *keyTab) isString() bool              { return t.loaded && t.info.Exists && t.info.Type == "string" }

func (t *keyTab) SetSize(w, h int) {
	t.width, t.height = max(w, 0), max(h, 0)
	body := max(h-len(t.headerLines()), 0)
	t.view.SetSize(w, body)
	t.grid.SetSize(w, body)
}

func (t *keyTab) setStatus(s string, isErr bool) { t.status, t.statusE = s, isErr }

// load reads the key's info and first page (first), or the next page.
func (t *keyTab) load(first bool) tea.Cmd {
	t.loading = true
	id, b, conn, n, key, size, match := t.id, t.backend, t.conn, t.db, t.key, int64(t.pageSize), t.match
	info, next, start, after := t.info, t.next, t.nextStart, t.nextAfter
	if first {
		next, start, after = 0, 0, ""
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := keyLoadedMsg{tabID: id, conn: conn, first: first}
		msg.err = withRedis(ctx, b, conn, n, func(c rdb.Client) error {
			if first {
				var err error
				if info, err = c.KeyInfo(ctx, key); err != nil {
					return err
				}
				msg.info = info
				if !info.Exists {
					return nil
				}
			}
			return readKeyPage(ctx, c, key, info, &msg, next, start, after, match, size)
		})
		return msg
	}
}

func readKeyPage(ctx context.Context, c rdb.Client, key string, info rdb.KeyInfo, msg *keyLoadedMsg,
	next uint64, start int64, after, match string, size int64,
) error {
	var cols []string
	var rows [][]string
	switch info.Type {
	case "string":
		v, err := c.GetString(ctx, key, maxStringRead)
		msg.text = v
		return err
	case "hash":
		kv, cur, err := c.HashPage(ctx, key, next, match, size)
		if err != nil {
			return err
		}
		cols = []string{"field", "value"}
		for _, fv := range kv {
			rows = append(rows, []string{fv[0], fv[1]})
		}
		msg.next, msg.more = cur, cur != 0
	case "set":
		ms, cur, err := c.SetPage(ctx, key, next, match, size)
		if err != nil {
			return err
		}
		cols = []string{"member"}
		for _, m := range ms {
			rows = append(rows, []string{m})
		}
		msg.next, msg.more = cur, cur != 0
	case "list":
		items, err := c.ListPage(ctx, key, start, size)
		if err != nil {
			return err
		}
		cols = []string{"index", "value"}
		for i, v := range items {
			rows = append(rows, []string{strconv.FormatInt(start+int64(i), 10), v})
		}
		msg.nextStart = start + int64(len(items))
		msg.more = int64(len(items)) == size && msg.nextStart < info.Len
	case "zset":
		zs, err := c.ZSetPage(ctx, key, start, size)
		if err != nil {
			return err
		}
		cols = []string{"rank", "member", "score"}
		for i, z := range zs {
			rows = append(rows, []string{strconv.FormatInt(start+int64(i), 10), z.Member, strconv.FormatFloat(z.Score, 'g', -1, 64)})
		}
		msg.nextStart = start + int64(len(zs))
		msg.more = int64(len(zs)) == size && msg.nextStart < info.Len
	case "stream":
		es, err := c.StreamPage(ctx, key, after, size)
		if err != nil {
			return err
		}
		cols = []string{"id", "fields"}
		for _, e := range es {
			parts := make([]string, len(e.Fields))
			for i, fv := range e.Fields {
				parts[i] = fv[0] + "=" + fv[1]
			}
			rows = append(rows, []string{e.ID, strings.Join(parts, " ")})
		}
		msg.nextAfter = after
		if len(es) > 0 {
			msg.nextAfter = es[len(es)-1].ID
		}
		msg.more = int64(len(es)) == size
	default:
		return nil // module types are not read
	}
	rs := &db.ResultSet{Columns: make([]db.ColumnInfo, len(cols))}
	for i, c := range cols {
		rs.Columns[i] = db.ColumnInfo{Name: c}
	}
	for _, r := range rows {
		vals := make([]db.Value, len(r))
		for i, v := range r {
			vals[i] = db.Value{Text: v}
		}
		rs.Rows = append(rs.Rows, vals)
	}
	msg.rs = rs
	return nil
}

func (t *keyTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case keyLoadedMsg:
		t.apply(m)
	case cellEditedMsg:
		t.cellEdited(m)
	case keyFilterMsg:
		t.match = rcmd.Pattern(m.text)
		return t.load(true)
	}
	return nil
}

func (t *keyTab) apply(m keyLoadedMsg) {
	t.loading = false
	if m.err != nil {
		t.err = m.err.Error()
		return
	}
	t.err = ""
	if m.first {
		t.info, t.loaded = m.info, true
		t.table, t.text, t.more = nil, "", false
		t.grid.SetResult(nil)
	}
	if !t.info.Exists {
		t.SetSize(t.width, t.height)
		return
	}
	if t.info.Type == "string" {
		t.text = m.text
		t.render()
	}
	if m.rs != nil {
		if m.first || t.table == nil {
			t.table = edit.New(m.rs.Columns, t.unseen(m.rs.Rows), nil)
			t.grid.SetResult(escapedResult(t.table.Result()))
		} else {
			t.table.Append(t.unseen(m.rs.Rows), nil)
			t.showChanges()
		}
	}
	t.next, t.nextStart, t.nextAfter, t.more = m.next, m.nextStart, m.nextAfter, m.more
	t.SetSize(t.width, t.height)
}

// unseen drops the hash fields and set members already loaded: HSCAN and
// SSCAN may return one again on a later page.
func (t *keyTab) unseen(rows [][]db.Value) [][]db.Value {
	if t.info.Type != "hash" && t.info.Type != "set" {
		return rows
	}
	seen := map[string]bool{}
	if t.table != nil {
		for i := range t.table.Len() {
			if r := t.table.Row(i); r.Orig != nil {
				seen[r.Orig[0].Text] = true
			}
		}
	}
	out := rows[:0:0]
	for _, r := range rows {
		if !seen[r[0].Text] {
			seen[r[0].Text] = true
			out = append(out, r)
		}
	}
	return out
}

func (t *keyTab) render() {
	lines, used := renderValue(t.text, t.format)
	t.used = used
	t.view.SetLines(lines)
}

// truncated reports whether only the first maxStringRead bytes were read.
func (t *keyTab) truncated() bool { return t.isString() && t.info.Len > maxStringRead }

func (t *keyTab) Find(q string, forward bool) bool {
	if t.isString() {
		return t.view.Find(q, forward)
	}
	return t.grid.Find(q, forward)
}

func (t *keyTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	if t.isString() {
		switch a {
		case keymap.Down:
			t.view.Move(1)
		case keymap.Up:
			t.view.Move(-1)
		case keymap.Top:
			t.view.Top()
		case keymap.Bottom:
			t.view.Bottom()
		case keymap.HalfPageDown:
			t.view.HalfPage(1)
		case keymap.HalfPageUp:
			t.view.HalfPage(-1)
		case keymap.ValueFormat:
			t.format = (t.format + 1) % (fmtHex + 1)
			t.render()
		}
		if a != keymap.Refresh {
			return nil
		}
	}
	if a == keymap.CellDetail {
		if r, c := t.grid.Position(); t.table != nil && r < t.table.Len() {
			col, _ := t.grid.Column()
			return openOverlay(overlay.NewDetail(col.Name, t.table.Row(r).Values[c].Text))
		}
		return nil
	}
	if cmd, ok := t.handleEdit(a); ok {
		return cmd
	}
	if cmd, ok := gridAction(t.grid, a); ok {
		return cmd
	}
	switch a {
	case keymap.Refresh:
		if !t.loading {
			return t.load(true)
		}
	case keymap.NextPage:
		switch {
		case t.loading || !t.loaded || !t.info.Exists:
		case !t.more:
			t.setStatus("no more", false)
		default:
			return t.load(false)
		}
	case keymap.FilterPrompt:
		if t.info.Type != "hash" && t.info.Type != "set" {
			t.setStatus("only hash fields and set members can be filtered", true)
			return nil
		}
		what := "fields"
		if t.info.Type == "set" {
			what = "members"
		}
		id := t.id
		return openOverlay(overlay.NewPrompt("Filter "+what, "Pattern", t.match, func(s string) tea.Msg {
			return keyFilterMsg{tabID: id, text: s}
		}))
	case keymap.FilterClear:
		if t.match != "" {
			t.match = ""
			return t.load(true)
		}
	}
	return nil
}

// header is "hash · TTL 3593s · 12 fields · 1.2 KB · listpack".
func (t *keyTab) header() string {
	if !t.loaded {
		return "loading…"
	}
	i := t.info
	if !i.Exists {
		return "key no longer exists"
	}
	parts := []string{i.Type}
	if i.TTL < 0 {
		parts = append(parts, "no expiry")
	} else {
		parts = append(parts, "TTL "+strconv.FormatInt(int64(i.TTL/time.Second), 10)+"s")
	}
	if i.Len >= 0 {
		unit := map[string]string{"string": "bytes", "hash": "fields", "set": "members"}[i.Type]
		if unit == "" {
			unit = "items"
		}
		parts = append(parts, groupDigits(i.Len)+" "+unit)
	}
	if i.Memory >= 0 {
		parts = append(parts, humanBytes(i.Memory))
	}
	if i.Encoding != "" {
		parts = append(parts, i.Encoding)
	}
	if i.Type == "string" {
		f := t.format.String()
		if t.format == fmtAuto {
			f += ": " + t.used.String()
		}
		parts = append(parts, f)
	}
	if t.match != "" {
		parts = append(parts, "filter "+rcmd.Escape(t.match))
	}
	if n := t.changes(); n > 0 {
		parts = append(parts, plural(n, "unsaved change"))
	}
	return strings.Join(parts, " · ")
}

func (t *keyTab) headerLines() []string {
	lines := []string{t.header()}
	switch {
	case t.err != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.err)))
	case t.status != "":
		st := theme.Muted
		if t.statusE {
			st = theme.Error
		}
		lines = append(lines, st.Render(textutil.Sanitize(t.status)))
	}
	if t.truncated() {
		lines = append(lines, theme.Muted.Render("showing first 1 MB of "+strconv.FormatInt(t.info.Len, 10)))
	}
	if t.loaded && t.info.Exists && !t.isString() && t.grid.Result() == nil {
		lines = append(lines, theme.Muted.Render(t.info.Type+" keys can be read in the console (coming in a later version)"))
	}
	return lines
}

func (t *keyTab) View(focused bool) string {
	t.SetSize(t.width, t.height) // the header grows with a status line
	head := t.headerLines()
	for i, l := range head {
		head[i] = textutil.Fit(l, t.width)
	}
	body := ""
	switch {
	case !t.loaded || !t.info.Exists:
	case t.isString():
		body = t.view.View(focused)
	case t.grid.Result() != nil:
		body = t.grid.View(focused)
	}
	if body == "" {
		return strings.Join(head, "\n")
	}
	return strings.Join(head, "\n") + "\n" + body
}

// humanBytes is 1234 -> "1.2 KB".
func humanBytes(n int64) string {
	switch {
	case n < 1<<10:
		return strconv.FormatInt(n, 10) + " B"
	case n < 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KB"
	case n < 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	}
	return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
}
