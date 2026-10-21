package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

const (
	// esDocLimit is how many documents a Documents view keeps.
	esDocLimit = 10_000
	// esDefaultColumns is how many mapped fields a view starts with.
	esDefaultColumns = 12
	// esReleaseTimeout bounds releasing a PIT or scroll.
	esReleaseTimeout = 5 * time.Second
)

// esTotalTimeout is how long a first page may take counting every hit;
// past it the count stops at 10,000 (spec §5.4).
var esTotalTimeout = 5 * time.Second

// esPageMsg is a page of documents; the first one carries the cursor it
// was read with.
type esPageMsg struct {
	tabID  int
	conn   string
	gen    int
	first  bool
	cursor es.Cursor
	page   es.Page
	err    error
}

func (m esPageMsg) TabID() int               { return m.tabID }
func (m esPageMsg) esMsg()                   {}
func (m esPageMsg) failure() (string, error) { return m.conn, m.err }

// orphanCursor is the cursor a first page opened for an Index tab closed
// since.
func orphanCursor(msg tea.Msg) es.Cursor {
	if m, ok := msg.(esPageMsg); ok {
		return m.cursor
	}
	return nil
}

// releaseCursor closes c in the background; failing only logs (spec §5.4).
func releaseCursor(c es.Cursor) {
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), esReleaseTimeout)
	defer cancel()
	_ = c.Close(ctx)
}

// esDocs is the Documents section of an Index tab.
type esDocs struct {
	tab    *esIndexTab
	grid   *grid.Model
	cursor es.Cursor
	hits   []esx.Hit
	cols   []string
	query  string // the filter as typed: a query string or a {…} DSL object
	// prevQuery is the filter before the prompt's, put back when the
	// server refuses the new one (fromPrompt).
	prevQuery  string
	fromPrompt bool
	sortCol    string // the column sorted on, "" for the default order
	sortDesc   bool
	total      int64
	exact      bool
	done       bool
	started    bool
	loading    bool
	loaded     bool
	err        string
	gen        int // bumped by every reload: pages of an older one are dropped
	limit      int
	dropped    int // rows dropped off the top so far

	changes  map[string]*esChange // staged, by docKey ("new\x00n" for new documents)
	order    []string             // the keys of changes in staging order
	ops      []esOp               // undo
	creates  int                  // new documents staged so far
	editing  string               // the document $EDITOR has open; "" a new one
	editPath string               // that temp file
	masked   map[string]bool      // the documents (docKey) shown with masked fields
}

func newESDocs(t *esIndexTab) *esDocs {
	d := &esDocs{tab: t, grid: grid.New(nil, t.settings.MaxColumnWidth), limit: esDocLimit}
	d.grid.SetHighlight(d.cellWords)
	d.grid.SetMarks(d.mark)
	return d
}

// pageSize is the connection's params.page_size, 100 by default.
func pageSize(cfg config.Connection) int {
	if n, err := strconv.Atoi(cfg.Params["page_size"]); err == nil && n > 0 {
		return n
	}
	return 100
}

// sortFields is the sort sent: the column chosen, else _score with a
// filter, else @timestamp when mapped, else index order.
func (d *esDocs) sortFields() []es.SortField {
	switch {
	case d.sortCol != "":
		f := d.sortCol
		if k := d.keywordOf(f); k != "" {
			f = k
		}
		return []es.SortField{{Field: f, Desc: d.sortDesc}}
	case strings.TrimSpace(d.query) != "":
		return []es.SortField{{Field: "_score", Desc: true}}
	case esx.TypeFor(d.tab.fields, "@timestamp") != "":
		return []es.SortField{{Field: "@timestamp", Desc: true}}
	}
	return nil
}

func (d *esDocs) keywordOf(path string) string {
	for _, f := range d.tab.fields {
		if f.Path == path {
			return f.Keyword
		}
	}
	return ""
}

// sortLabel is the header's sort.
func (d *esDocs) sortLabel() string {
	s := d.sortFields()
	if len(s) == 0 {
		return "_doc"
	}
	if s[0].Desc {
		return s[0].Field + " ↓"
	}
	return s[0].Field + " ↑"
}

// reload drops what is shown and reads from the top, releasing the old
// cursor.
func (d *esDocs) reload() tea.Cmd {
	t := d.tab
	query, err := esx.SearchQuery(d.query)
	if err != nil {
		d.err = err.Error()
		return nil
	}
	old := d.cursor
	d.cursor, d.hits, d.done, d.dropped, d.err = nil, nil, false, 0, ""
	d.gen++
	d.started, d.loading = true, true
	cfg, _ := t.backend.Connection(t.conn)
	spec := es.SearchSpec{
		Target: t.target, Query: query, Sort: d.sortFields(), PageSize: pageSize(cfg),
		Highlight: strings.TrimSpace(d.query) != "", ExactTotal: true,
	}
	tabCtx, b, id, conn, gen := t.ctx, t.backend, t.id, t.conn, d.gen
	return func() tea.Msg {
		releaseCursor(old)
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		msg := esPageMsg{tabID: id, conn: conn, gen: gen, first: true}
		msg.err = withES(ctx, b, conn, func(doer es.Doer) error {
			info, _ := b.ESInfo(conn)
			cur := es.OpenCursor(info, doer, spec)
			tctx, tcancel := context.WithTimeout(ctx, esTotalTimeout)
			page, err := cur.Next(tctx)
			tcancel()
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				releaseCursor(cur) // counting took too long: stop at 10,000
				spec.ExactTotal = false
				cur = es.OpenCursor(info, doer, spec)
				page, err = cur.Next(ctx)
			}
			if err != nil {
				releaseCursor(cur)
				return err
			}
			msg.cursor, msg.page = cur, page
			return nil
		})
		return msg
	}
}

// loadMore reads the next page.
func (d *esDocs) loadMore() tea.Cmd {
	t := d.tab
	switch {
	case d.loading:
		return nil
	case d.done:
		t.setStatus("end of results", false)
		return nil
	case d.cursor == nil:
		return d.reload()
	}
	d.loading = true
	cur, tabCtx, id, conn, gen := d.cursor, t.ctx, t.id, t.conn, d.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		page, err := cur.Next(ctx)
		return esPageMsg{tabID: id, conn: conn, gen: gen, page: page, err: err}
	}
}

// close releases the cursor.
func (d *esDocs) close() func() {
	cur := d.cursor
	d.cursor = nil
	d.gen++
	return func() { releaseCursor(cur) }
}

func (d *esDocs) paged(m esPageMsg) tea.Cmd {
	t := d.tab
	if m.gen != d.gen {
		if cur := m.cursor; cur != nil {
			return func() tea.Msg { releaseCursor(cur); return nil }
		}
		return nil
	}
	d.loading = false
	if m.err != nil {
		if reason, ok := queryRefused(m.err); ok && d.fromPrompt {
			failed := d.query
			d.query, d.fromPrompt = d.prevQuery, false
			return tea.Batch(d.reload(), d.prompt(failed, reason))
		}
		if errors.Is(m.err, es.ErrCursorExpired) {
			t.setStatus("the result cursor expired; reloading from the top", true)
			return d.reload()
		}
		d.err = esReason(m.err)
		return nil
	}
	d.loaded = true
	if m.first {
		d.fromPrompt = false
		d.cursor, d.total, d.exact = m.cursor, m.page.Total, m.page.TotalExact
		d.cols = d.columns()
	}
	d.done = m.page.Done
	row, col := d.grid.Position()
	for _, h := range m.page.Hits {
		h, masked := t.masker.MaskHit(h)
		if masked {
			if d.masked == nil {
				d.masked = map[string]bool{}
			}
			d.masked[hitKey(h)] = true
		}
		d.hits = append(d.hits, h)
	}
	if over := len(d.hits) - d.limit; over > 0 {
		d.hits = d.hits[over:]
		d.dropped += over
		row = max(row-over, 0)
		t.setStatus(fmt.Sprintf("dropped the first %s rows (keeping %s)", groupDigits(int64(d.dropped)), groupDigits(int64(d.limit))), false)
	} else if !m.first && d.done {
		t.setStatus("end of results", false)
	}
	if m.first {
		d.grid.SetResult(d.result())
		if col < len(d.cols) {
			d.grid.SetCursor(0, col) // a re-sort or new filter stays on its column
		}
		return nil
	}
	d.grid.ReplaceResult(d.result())
	d.grid.SetCursor(row, col)
	return nil
}

// multi reports a view over more than one index: the _index column shows.
func (d *esDocs) multi() bool { return d.tab.kind != targetIndex || len(d.tab.mappings) > 1 }

func (d *esDocs) columns() []string {
	cols := []string{"_id"}
	if d.multi() {
		cols = append(cols, "_index")
	}
	return append(cols, esx.DefaultColumns(d.tab.fields, esDefaultColumns)...)
}

// cell is a hit's value of col for the grid.
func cell(h esx.Hit, col string) db.Value {
	switch col {
	case "_id":
		return db.Value{Text: h.ID}
	case "_index":
		return db.Value{Text: h.Index}
	case "_score":
		if h.Score == nil {
			return db.Value{Null: true}
		}
		return db.Value{Text: strconv.FormatFloat(*h.Score, 'f', -1, 64)}
	}
	v, ok := esx.GetPath(h.Source, col)
	text, null := esx.CellText(v, ok)
	return db.Value{Text: text, Null: null}
}

// result is the hits as rows; with search hits in fields that are not
// columns, a last "+" column names them.
func (d *esDocs) result() *db.ResultSet {
	extra := make([]string, len(d.hits))
	anyExtra := false
	for r, h := range d.hits {
		extra[r] = extraHits(h, d.cols)
		anyExtra = anyExtra || extra[r] != ""
	}
	rs := &db.ResultSet{Columns: make([]db.ColumnInfo, len(d.cols)), Rows: make([][]db.Value, len(d.hits))}
	for i, c := range d.cols {
		rs.Columns[i] = db.ColumnInfo{Name: c, DatabaseType: esx.TypeFor(d.tab.fields, c)}
	}
	if anyExtra {
		rs.Columns = append(rs.Columns, db.ColumnInfo{Name: "+"})
	}
	for r, h := range d.hits {
		row := make([]db.Value, len(d.cols), len(rs.Columns))
		v := d.view(h)
		for i, c := range d.cols {
			row[i] = cell(v, c)
		}
		if anyExtra {
			row = append(row, db.Value{Text: extra[r]})
		}
		rs.Rows[r] = row
	}
	return rs
}

// cellWords are the search hits to mark in a cell.
func (d *esDocs) cellWords(row, col int) []string {
	if row >= len(d.hits) || col >= len(d.cols) {
		return nil
	}
	return hitWords(d.hits[row], d.cols[col])
}

// selected is the hit under the cursor.
func (d *esDocs) selected() (esx.Hit, bool) {
	r, _ := d.grid.Position()
	if r < 0 || r >= len(d.hits) {
		return esx.Hit{}, false
	}
	return d.hits[r], true
}

// column is the column under the cursor.
func (d *esDocs) column() string {
	_, c := d.grid.Position()
	if c < 0 || c >= len(d.cols) {
		return ""
	}
	return d.cols[c]
}

// toggleSort cycles field through ascending, descending and off.
func (d *esDocs) toggleSort(field string) error {
	switch {
	case field == "_id" || field == "_index":
		return fmt.Errorf("cannot sort on %s", field)
	case esx.TypeFor(d.tab.fields, field) == "text" && d.keywordOf(field) == "":
		return fmt.Errorf("cannot sort on text field; use %s.keyword", field)
	}
	switch {
	case d.sortCol != field:
		d.sortCol, d.sortDesc = field, false
	case !d.sortDesc:
		d.sortDesc = true
	default:
		d.sortCol, d.sortDesc = "", false
	}
	return nil
}

// header is "q: <query>  <N> hits  sort: <field>".
func (d *esDocs) header() string {
	var parts []string
	if q := strings.TrimSpace(d.query); q != "" {
		parts = append(parts, "q: "+q)
	}
	hits := groupDigits(d.total) + " hits"
	if !d.exact {
		hits = groupDigits(d.total) + "+ hits"
	}
	parts = append(parts, hits, "sort: "+d.sortLabel())
	if d.dropped > 0 {
		parts = append(parts, fmt.Sprintf("rows %s–%s", groupDigits(int64(d.dropped+1)), groupDigits(int64(d.dropped+len(d.hits)))))
	}
	return strings.Join(parts, "  ")
}

// handle is a key on the Documents section; ok is false for keys it
// leaves to the caller.
func (d *esDocs) handle(a keymap.Action) (tea.Cmd, bool) {
	t := d.tab
	row, _ := d.grid.Position()
	switch a {
	case keymap.Down, keymap.HalfPageDown:
		if row >= len(d.hits)-1 && len(d.hits) > 0 {
			return d.loadMore(), true
		}
	case keymap.LoadMore:
		return d.loadMore(), true
	case keymap.Refresh:
		return d.reload(), true
	case keymap.SortColumn:
		if err := d.toggleSort(d.column()); err != nil {
			t.setStatus(err.Error(), true)
			return nil, true
		}
		return d.reload(), true
	case keymap.DeleteByQuery:
		return d.deleteByQuery(), true
	case keymap.UpdateByQuery:
		return d.updateByQuery(), true
	case keymap.SimulatePipeline:
		return d.simulate(), true
	case keymap.EditCell:
		return d.editCell(), true
	case keymap.InsertRow:
		return d.externalEdit(true), true
	case keymap.ExternalEdit:
		return d.externalEdit(false), true
	case keymap.DeleteRow:
		d.deleteRow()
		return nil, true
	case keymap.UndoEdit:
		d.undo()
		return nil, true
	case keymap.UndoAllEdits:
		d.undoAll()
		return nil, true
	case keymap.SubmitEdits:
		return d.submit(), true
	case keymap.FilterPrompt:
		return d.prompt(d.query, ""), true
	case keymap.FilterEqual, keymap.FilterNotEqual, keymap.FilterContains, keymap.FilterGreater,
		keymap.FilterLess, keymap.FilterNull, keymap.FilterNotNull, keymap.FilterClear:
		return d.cellFilter(a), true
	case keymap.CellDetail:
		if row < len(d.hits) {
			return openOverlay(newESDocDetail(t, row)), true
		}
		return nil, true
	}
	return nil, false
}

// yankText copies the document under the cursor: its _id, _source, as
// console requests or as curl.
func (d *esDocs) yankText(a keymap.Action) (text, what string, ok bool) {
	h, found := d.selected()
	if !found {
		return "", "", false
	}
	note := ""
	if h.Truncated {
		note = " (a preview: the document is over 1 MB)"
	}
	switch a {
	case keymap.YankID:
		return h.ID, "_id", true
	case keymap.YankSource:
		return textutil.PrettyJSON(string(h.Source)), "_source" + note, true
	case keymap.YankRequest:
		path := h.Index + "/_doc/" + h.ID
		return "GET " + path + "\n\nPUT " + path + "\n" + textutil.PrettyJSON(string(h.Source)) + "\n", "the requests" + note, true
	case keymap.YankCurl:
		cfg, _ := d.tab.backend.Connection(d.tab.conn)
		return curlFor(cfg, "GET", h.Index+"/_doc/"+h.ID, nil), "curl", true
	}
	return "", "", false
}

// curlFor is a request as curl without credentials: -u with the user
// (curl asks for the password) or an API key placeholder.
func curlFor(cfg config.Connection, method, path string, body []byte) string {
	base := "http://localhost:9200"
	if n, err := config.Normalize(cfg); err == nil {
		cfg = n
	}
	if urls, err := config.ESURLs(cfg); err == nil {
		base = urls[0]
	}
	parts := []string{"curl"}
	if cfg.Params["tls_skip_verify"] == "true" {
		parts = append(parts, "-k")
	}
	switch {
	case cfg.Params["auth"] == "apikey":
		parts = append(parts, "-H 'Authorization: ApiKey <api-key>'")
	case cfg.User != "":
		parts = append(parts, "-u "+shellQuote(cfg.User))
	}
	parts = append(parts, "-X "+method, shellQuote(base+"/"+strings.TrimLeft(path, "/")))
	if len(body) > 0 {
		parts = append(parts, "-H 'Content-Type: application/json'", "-d "+shellQuote(string(body)))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ndjson is the documents as NDJSON rows: {"_index","_id","_source"}.
func (d *esDocs) ndjson() *db.ResultSet {
	rs := &db.ResultSet{Columns: []db.ColumnInfo{{Name: "document", DatabaseType: "JSON"}}}
	for _, h := range d.hits {
		b, _ := json.Marshal(struct {
			Index  string          `json:"_index"`
			ID     string          `json:"_id"`
			Source json.RawMessage `json:"_source"`
		}{h.Index, h.ID, h.Source})
		rs.Rows = append(rs.Rows, []db.Value{{Text: string(b)}})
	}
	return rs
}

// highlightWords are the words a hit's highlight fragments mark with
// <em>…</em>.
func highlightWords(h esx.Hit) []string {
	var out []string
	for _, frags := range h.Highlight {
		for _, f := range frags {
			for {
				start := strings.Index(f, "<em>")
				if start < 0 {
					break
				}
				end := strings.Index(f[start:], "</em>")
				if end < 0 {
					break
				}
				if w := f[start+4 : start+end]; w != "" && !slices.Contains(out, w) {
					out = append(out, w)
				}
				f = f[start+end+5:]
			}
		}
	}
	return out
}

// markWords draws the words in s in the search highlight color.
func markWords(s string, words []string) string {
	lower := strings.ToLower(s)
	if len(words) == 0 || len(lower) != len(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		hit := ""
		for _, w := range words {
			if w != "" && strings.HasPrefix(lower[i:], strings.ToLower(w)) && len(w) > len(hit) {
				hit = s[i : i+len(w)]
			}
		}
		if hit == "" {
			b.WriteByte(s[i])
			i++
			continue
		}
		b.WriteString(theme.Visual.Render(hit))
		i += len(hit)
	}
	return b.String()
}
