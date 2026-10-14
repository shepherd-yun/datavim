package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/complete"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/structure"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

const (
	loadTimeout  = 15 * time.Second
	queryTimeout = 30 * time.Second
)

// tab is one entry in the right-hand tab area.
type tab interface {
	ID() int
	Title() string
	Key() string
	Context() keymap.Context
	Target() (conn string, ref db.TableRef)
	Init() tea.Cmd
	SetSize(w, h int)
	Handle(a keymap.Action) tea.Cmd
	Update(msg tea.Msg) tea.Cmd
	View(focused bool) string
}

// framer is a tab that draws the tab area's frame itself, title and all:
// the console frames its editor and its results apart so that the focused
// one stands out.
type framer interface {
	Framed(title string, focused, danger bool) string
}

// tabMsg is an async result addressed to one tab.
type tabMsg interface{ TabID() int }

// openOverlayMsg asks the root model to show a popup.
type openOverlayMsg struct{ overlay overlay.Overlay }

func openOverlay(o overlay.Overlay) tea.Cmd {
	return func() tea.Msg { return openOverlayMsg{overlay: o} }
}

func tableKey(conn string, ref db.TableRef) string {
	return strings.Join([]string{conn, ref.Database, ref.Schema, ref.Name}, "\x00")
}

func dataTabKey(conn string, ref db.TableRef) string { return "data\x00" + tableKey(conn, ref) }

func structureTabKey(conn string, ref db.TableRef) string { return "struct\x00" + tableKey(conn, ref) }

// ---- data tab ----

type pageLoadedMsg struct {
	tabID   int
	conn    string
	page    int
	query   tableQuery
	rs      *db.ResultSet
	hasMore bool
	meta    *tableMeta // read with the page when the tab did not know it yet
	ids     []string   // PostgreSQL ctids, when rows are located by them
	err     error
}

func (m pageLoadedMsg) TabID() int { return m.tabID }

// filterMsg is a filter typed at the F prompt.
type filterMsg struct {
	tabID int
	text  string
}

func (m filterMsg) TabID() int { return m.tabID }

// normFilter trims a typed filter and its trailing semicolons.
func normFilter(s string) string {
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), ";"))
}

// filterValueMsg is a value typed for fs, fl, f> or f<.
type filterValueMsg struct {
	tabID  int
	action keymap.Action
	col    db.ColumnInfo
	text   string
}

func (m filterValueMsg) TabID() int { return m.tabID }

type dataTab struct {
	id       int
	backend  Backend
	conn     string
	ref      db.TableRef
	dialect  sqlx.Dialect
	pageSize int
	grid     *grid.Model

	page        int
	hasMore     bool
	loading     bool
	err         string
	status      string // what the last key did; cleared by the next one
	statusErr   bool
	query       tableQuery
	known       bool        // meta (key, columns) has been read
	cols        []db.Column // the table's columns, for types in the cell editor
	key         edit.Key    // how rows are located
	table       *edit.Table // the page with its staged changes
	store       Store
	submitting  bool     // a submit is running
	gen         int      // tells the current preview from stale ones
	pendingSQL  []string // the previewed statements, until confirmed
	retrySubmit bool     // the last submit wanted a password
	height      int
	wantColumn  string // column to put the cursor on once data is shown
	failedWhere string // a filter the server rejected, offered again by F
}

func newDataTab(id int, b Backend, st Store, conn string, ref db.TableRef, s config.Settings) *dataTab {
	cfg, _ := b.Connection(conn)
	t := &dataTab{
		id: id, backend: b, store: st, conn: conn, ref: ref, dialect: sqlx.DialectOf(cfg.Driver), pageSize: s.PageSize,
		grid: grid.New(nil, s.MaxColumnWidth),
	}
	t.grid.SetMarks(t.mark)
	return t
}

func (t *dataTab) ID() int                       { return t.id }
func (t *dataTab) Title() string                 { return t.ref.Name }
func (t *dataTab) Key() string                   { return dataTabKey(t.conn, t.ref) }
func (t *dataTab) Context() keymap.Context       { return keymap.Grid }
func (t *dataTab) Target() (string, db.TableRef) { return t.conn, t.ref }
func (t *dataTab) Init() tea.Cmd                 { return t.load(0, t.query) }

func (t *dataTab) SetSize(w, h int) {
	t.height = max(h, 0)
	t.grid.SetSize(w, max(h-1, 0))
}

func (t *dataTab) setStatus(s string, isErr bool) { t.status, t.statusErr = s, isErr }

// load reads page of q. q becomes the tab's query only once it loaded, so
// a failing filter leaves the rows on screen.
func (t *dataTab) load(page int, q tableQuery) tea.Cmd {
	t.loading = true
	id, b, conn, ref, size, d, known := t.id, t.backend, t.conn, t.ref, t.pageSize, t.dialect, t.known
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := pageLoadedMsg{tabID: id, conn: conn, page: page, query: q}
		msg.err = withConn(ctx, b, conn, ref.Database, func(c db.Conn) error {
			if !known {
				meta, err := readTableMeta(ctx, c.Meta(), d, ref)
				if err != nil {
					return err
				}
				msg.meta = meta
				msg.query.key = meta.order()
				msg.query.rowID = meta.key.Kind == edit.RowID
			}
			var err error
			msg.rs, msg.ids, msg.hasMore, err = fetchPage(ctx, c, d, ref, msg.query, page, size)
			return err
		})
		return msg
	}
}

func primaryKey(cols []db.Column) []string {
	var out []string
	for _, c := range cols {
		if c.PrimaryKey {
			out = append(out, c.Name)
		}
	}
	return out
}

// tableMeta is what editing needs from the catalog.
type tableMeta struct {
	cols []db.Column
	key  edit.Key
}

func readTableMeta(ctx context.Context, m db.MetaReader, d sqlx.Dialect, ref db.TableRef) (*tableMeta, error) {
	cols, err := m.Columns(ctx, ref)
	if err != nil {
		return nil, err
	}
	idx, err := m.Indexes(ctx, ref)
	if err != nil {
		return nil, err
	}
	view := false
	if d == sqlx.Postgres && len(primaryKey(cols)) == 0 {
		tables, err := m.Tables(ctx, ref.Namespace)
		if err != nil {
			return nil, err
		}
		for _, tb := range tables {
			if tb.Name == ref.Name {
				view = tb.Kind == db.TableKindView
			}
		}
	}
	return &tableMeta{cols: cols, key: edit.ChooseKey(d, cols, idx, view)}, nil
}

// order is the columns that keep pages stable: the key's, if it has any.
func (tm *tableMeta) order() []string {
	if tm.key.Kind == edit.PrimaryKey || tm.key.Kind == edit.UniqueKey {
		return tm.key.Columns
	}
	return nil
}

// fetchPage reads one page; with q.rowID the leading ctid column is split
// off into ids.
func fetchPage(ctx context.Context, c db.Conn, d sqlx.Dialect, ref db.TableRef, q tableQuery, page, size int) (*db.ResultSet, []string, bool, error) {
	rs, err := c.Query(ctx, c.Dialect().Paginate(q.sql(d, ref), size+1, page*size))
	if err != nil {
		return nil, nil, false, err
	}
	hasMore := len(rs.Rows) > size
	if hasMore {
		rs.Rows = rs.Rows[:size]
	}
	var ids []string
	if q.rowID && len(rs.Columns) > 0 {
		ids = make([]string, len(rs.Rows))
		for i, row := range rs.Rows {
			ids[i] = row[0].Text
			rs.Rows[i] = row[1:]
		}
		rs.Columns = rs.Columns[1:]
	}
	return rs, ids, hasMore, nil
}

func (t *dataTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case pageLoadedMsg:
		t.pageLoaded(m)
	case cellEditedMsg:
		t.cellEdited(m)
	case submitConfirmedMsg:
		if m.gen == t.gen && t.pendingSQL != nil && !t.submitting {
			stmts := t.pendingSQL
			t.pendingSQL = nil
			return t.apply(stmts)
		}
	case editsAppliedMsg:
		return t.applied(m)
	case filterMsg:
		where := normFilter(m.text)
		return t.applyQuery(func(q *tableQuery) { q.where = where })
	case filterValueMsg:
		if m.text == "" {
			return nil
		}
		switch m.action {
		case keymap.FilterContains:
			return t.addFilter(containsFilter(t.dialect, m.col.Name, m.text))
		case keymap.FilterLike:
			return t.addFilter(likeFilter(t.dialect, m.col.Name, m.text))
		case keymap.FilterGreater:
			return t.addFilter(compareFilter(t.dialect, m.col, ">", db.Value{Text: m.text}))
		case keymap.FilterLess:
			return t.addFilter(compareFilter(t.dialect, m.col, "<", db.Value{Text: m.text}))
		}
	}
	return nil
}

func (t *dataTab) pageLoaded(m pageLoadedMsg) {
	t.loading = false
	if m.err != nil {
		t.err = m.err.Error()
		if m.query.where != t.query.where {
			t.failedWhere = m.query.where
		}
		return
	}
	t.failedWhere = ""
	t.err, t.page, t.hasMore = "", m.page, m.hasMore
	t.query, t.known = m.query, true
	if m.meta != nil {
		t.cols, t.key = m.meta.cols, m.meta.key
	}
	t.table = edit.New(m.rs.Columns, m.rs.Rows, m.ids)
	t.grid.SetSort(t.query.sort, t.query.desc)
	t.grid.SetResult(t.table.Result())

	if t.wantColumn != "" {
		t.grid.SelectColumn(t.wantColumn)
		t.wantColumn = ""
	}
}

// focusColumn puts the cursor on the column called name, now or once the
// data has loaded.
func (t *dataTab) focusColumn(name string) {
	if !t.grid.SelectColumn(name) {
		t.wantColumn = name
	}
}

// gridAction handles the cursor, scroll and cell-detail actions every grid shares.
func gridAction(g *grid.Model, a keymap.Action) (tea.Cmd, bool) {
	switch a {
	case keymap.Down:
		g.Move(1, 0)
	case keymap.Up:
		g.Move(-1, 0)
	case keymap.Left:
		g.Move(0, -1)
	case keymap.Right:
		g.Move(0, 1)
	case keymap.Top:
		g.Top()
	case keymap.Bottom:
		g.Bottom()
	case keymap.HalfPageDown:
		g.HalfPage(1)
	case keymap.HalfPageUp:
		g.HalfPage(-1)
	case keymap.LineStart:
		g.LineStart()
	case keymap.LineEnd:
		g.LineEnd()
	case keymap.ScrollLeft:
		g.ScrollColumns(-1)
	case keymap.ScrollRight:
		g.ScrollColumns(1)
	case keymap.ScrollHalfLeft:
		g.ScrollHalf(-1)
	case keymap.ScrollHalfRight:
		g.ScrollHalf(1)
	case keymap.CellDetail:
		if col, v, ok := g.Cell(); ok {
			text := v.Text
			if v.Null {
				text = "NULL"
			}
			return openOverlay(overlay.NewDetail(col.Name, text)), true
		}
	default:
		return nil, false
	}
	return nil, true
}

func (t *dataTab) Find(q string, forward bool) bool { return t.grid.Find(q, forward) }

func (t *dataTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	if cmd, ok := gridAction(t.grid, a); ok {
		return cmd
	}
	switch a {
	case keymap.NextPage, keymap.PrevPage, keymap.Refresh, keymap.FilterPrompt, keymap.SortColumn,
		keymap.FilterEqual, keymap.FilterNotEqual, keymap.FilterContains, keymap.FilterLike, keymap.FilterGreater,
		keymap.FilterLess, keymap.FilterNull, keymap.FilterNotNull, keymap.FilterClear:
		if n := t.changes(); n > 0 {
			t.setStatus(plural(n, "unsaved change")+" · <space>w submits, U discards", true)
			return nil
		}
	}

	switch a {
	case keymap.NextPage:
		if t.hasMore && !t.loading {
			return t.load(t.page+1, t.query)
		}
	case keymap.PrevPage:
		if t.page > 0 && !t.loading {
			return t.load(t.page-1, t.query)
		}
	case keymap.Refresh:
		if !t.loading {
			t.known = false
			return t.load(t.page, t.query)
		}
	case keymap.FilterEqual, keymap.FilterNotEqual:
		col, v, ok := t.grid.Cell()
		if !ok {
			t.setStatus("no cell to filter by", true)
			return nil
		}
		op := "="
		if a == keymap.FilterNotEqual {
			op = "<>"
		}
		return t.addFilter(compareFilter(t.dialect, col, op, v))
	case keymap.FilterNull, keymap.FilterNotNull:
		col, ok := t.grid.Column()
		if !ok {
			t.setStatus("no column to filter by", true)
			return nil
		}
		return t.addFilter(nullFilter(t.dialect, col.Name, a == keymap.FilterNotNull))
	case keymap.FilterContains, keymap.FilterLike, keymap.FilterGreater, keymap.FilterLess:
		return t.filterValuePrompt(a)
	case keymap.FilterClear:
		if t.query.where == "" {
			return nil
		}
		return t.applyQuery(func(q *tableQuery) { q.where = "" })
	case keymap.FilterPrompt:
		return t.filterPrompt()
	case keymap.SortColumn:
		col, ok := t.grid.Column()
		if !ok {
			return nil
		}
		return t.applyQuery(func(q *tableQuery) {
			switch {
			case q.sort != col.Name:
				q.sort, q.desc = col.Name, false
			case !q.desc:
				q.desc = true
			default:
				q.sort, q.desc = "", false
			}
		})
	case keymap.EditCell:
		return t.editCell()
	case keymap.InsertRow:
		t.insertRow()
	case keymap.DeleteRow:
		t.deleteRow()
	case keymap.UndoEdit:
		t.undoEdit()
	case keymap.UndoAllEdits:
		t.undoAll()
	case keymap.SubmitEdits:
		return t.submit()

	}
	return nil
}

// applyQuery reloads the first page with a changed filter or sort, keeping
// the cursor's column.
func (t *dataTab) applyQuery(change func(*tableQuery)) tea.Cmd {
	if t.loading {
		return nil
	}
	q := t.query
	change(&q)
	if err := q.check(t.dialect, t.ref); err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	if col, ok := t.grid.Column(); ok {
		t.wantColumn = col.Name
	}
	return t.load(0, q)
}

// addFilter narrows the filter with cond.
func (t *dataTab) addFilter(cond string) tea.Cmd {
	return t.applyQuery(func(q *tableQuery) { q.where = andFilter(q.where, cond) })
}

var filterVerbs = map[keymap.Action]string{
	keymap.FilterContains: "contains",
	keymap.FilterLike:     "LIKE",
	keymap.FilterGreater:  ">",
	keymap.FilterLess:     "<",
}

// filterValuePrompt asks for the value of fs, fl, f> or f<, starting from
// the cell under the cursor (wrapped in % for fl).
func (t *dataTab) filterValuePrompt(a keymap.Action) tea.Cmd {
	col, ok := t.grid.Column()
	if !ok {
		t.setStatus("no column to filter by", true)
		return nil
	}
	initial := ""
	if _, v, ok := t.grid.Cell(); ok && !v.Null {
		initial = v.Text
	}
	label := col.Name + " " + filterVerbs[a] + " …"
	if a == keymap.FilterLike {
		initial = "%" + initial + "%"
		label += "  % any text · _ one character"
	}
	id := t.id
	return openOverlay(overlay.NewPrompt("Filter "+t.ref.Name, label, initial,
		func(s string) tea.Msg { return filterValueMsg{tabID: id, action: a, col: col, text: s} }))
}

// filterPrompt edits the WHERE condition, starting from a filter the
// server just rejected, else the current one, else the cursor's column.
func (t *dataTab) filterPrompt() tea.Cmd {
	initial := t.query.where
	switch {
	case t.failedWhere != "":
		initial = t.failedWhere
	case initial == "":
		if col, ok := t.grid.Column(); ok {
			initial = sqlx.QuoteIfNeeded(col.Name, t.dialect) + " "
		}
	}
	id := t.id
	return openOverlay(overlay.NewPrompt("Filter "+t.ref.Name,
		"WHERE …  e.g. name LIKE '%abc%' AND age > 18 · empty shows every row", initial,
		func(s string) tea.Msg { return filterMsg{tabID: id, text: s} },
		overlay.WithCompleter(t.completeFilter),
		overlay.WithValidate(t.checkFilter)))
}

// checkFilter rejects a typed filter that is not a single condition.
func (t *dataTab) checkFilter(s string) error {
	q := t.query
	q.where = normFilter(s)
	return q.check(t.dialect, t.ref)
}

var filterKeywords = []string{"AND", "OR", "NOT", "LIKE", "IN", "IS", "NULL", "BETWEEN", "TRUE", "FALSE"}

func isFilterWordRune(r rune) bool {
	return r == '_' || r == '"' || r == '`' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// completeFilter offers the table's columns and condition keywords for the
// word before the cursor in the F prompt.
func (t *dataTab) completeFilter(text string, cursor int) (int, []complete.Candidate) {
	rs := []rune(text)
	cursor = min(max(cursor, 0), len(rs))
	start := cursor
	for start > 0 && isFilterWordRune(rs[start-1]) {
		start--
	}
	prefix := strings.Trim(string(rs[start:cursor]), "\"`")
	var cands []complete.Candidate
	for _, c := range t.cols {
		cands = append(cands, complete.Candidate{
			Text: sqlx.QuoteIfNeeded(c.Name, t.dialect), Label: c.Name, Kind: complete.Column, Detail: c.Type,
		})
	}
	kws := filterKeywords
	if t.dialect == sqlx.Postgres {
		kws = append(slices.Clone(kws), "ILIKE")
	}
	lower := strings.ToUpper(prefix) != prefix
	for _, kw := range kws {
		if lower {
			kw = strings.ToLower(kw)
		}
		cands = append(cands, complete.Candidate{Text: kw, Label: kw, Kind: complete.Keyword})
	}
	return start, complete.Rank(prefix, cands, maxCandidates)
}

func (t *dataTab) View(focused bool) string {
	return textutil.PadLines(t.grid.View(focused), max(t.height-1, 0)) + "\n" + t.statusLine()
}

func (t *dataTab) statusLine() string {
	switch {
	case t.loading:
		return theme.Muted.Render("loading…")
	case t.err != "":
		return theme.Error.Render("✗ " + textutil.Sanitize(t.err))
	case t.status != "":
		style := theme.Muted
		if t.statusErr {
			style = theme.Error
		}
		return style.Render(textutil.Sanitize(t.status))
	}
	info := textutil.Sanitize(t.pageInfo())
	if n := t.changes(); n > 0 {
		return theme.Accent.Render("● "+plural(n, "change")) + theme.Muted.Render(" · <space>w submit · U discard · "+info)
	}
	return theme.Muted.Render(info)
}

// pageInfo describes the rows shown: their numbers, filter, sort and paging.
func (t *dataTab) pageInfo() string {
	var parts []string
	if n := t.grid.RowCount(); n == 0 {
		parts = append(parts, "no rows")
	} else {
		first := t.page*t.pageSize + 1
		parts = append(parts, fmt.Sprintf("rows %d–%d", first, first+n-1))
	}
	parts = append(parts, fmt.Sprintf("page %d", t.page+1))
	if t.query.where != "" {
		parts = append(parts, "where "+oneLine(t.query.where))
	}
	if t.query.sort != "" {
		dir := "asc"
		if t.query.desc {
			dir = "desc"
		}
		parts = append(parts, "sorted by "+t.query.sort+" "+dir)
	}
	if t.hasMore {
		parts = append(parts, "] next")
	}
	if t.page > 0 {
		parts = append(parts, "[ prev")
	}
	return strings.Join(parts, " · ")
}

// ---- structure tab ----

type structureLoadedMsg struct {
	tabID int
	conn  string
	data  structure.Data
	err   error
}

func (m structureLoadedMsg) TabID() int { return m.tabID }

type structureTab struct {
	id      int
	backend Backend
	conn    string
	ref     db.TableRef
	view    *structure.Model
	loading bool
	err     string
}

func newStructureTab(id int, b Backend, conn string, ref db.TableRef) *structureTab {
	cfg, _ := b.Connection(conn)
	return &structureTab{id: id, backend: b, conn: conn, ref: ref, view: structure.New(sqlx.DialectOf(cfg.Driver))}
}

func (t *structureTab) ID() int                       { return t.id }
func (t *structureTab) Title() string                 { return t.ref.Name + ":struct" }
func (t *structureTab) Key() string                   { return structureTabKey(t.conn, t.ref) }
func (t *structureTab) Context() keymap.Context       { return keymap.Structure }
func (t *structureTab) Target() (string, db.TableRef) { return t.conn, t.ref }
func (t *structureTab) Init() tea.Cmd                 { return t.load() }
func (t *structureTab) SetSize(w, h int)              { t.view.SetSize(w, h) }

func (t *structureTab) load() tea.Cmd {
	t.loading = true
	id, b, conn, ref := t.id, t.backend, t.conn, t.ref
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := structureLoadedMsg{tabID: id, conn: conn}
		msg.err = withConn(ctx, b, conn, ref.Database, func(c db.Conn) error {
			var err error
			msg.data, err = fetchStructure(ctx, c.Meta(), ref)
			return err
		})
		return msg
	}
}

func fetchStructure(ctx context.Context, m db.MetaReader, ref db.TableRef) (structure.Data, error) {
	var d structure.Data
	var err error
	if d.DDL, err = m.DDL(ctx, ref); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return d, fmt.Errorf("table %s not found", ref.Name)
		}
		return d, err
	}
	if d.Columns, err = m.Columns(ctx, ref); err != nil {
		return d, err
	}
	if d.Indexes, err = m.Indexes(ctx, ref); err != nil {
		return d, err
	}
	d.ForeignKeys, err = m.ForeignKeys(ctx, ref)
	return d, err
}

func (t *structureTab) Update(msg tea.Msg) tea.Cmd {
	m, ok := msg.(structureLoadedMsg)
	if !ok {
		return nil
	}
	t.loading = false
	if m.err != nil {
		t.err = m.err.Error()
		return nil
	}
	t.err = ""
	t.view.SetData(m.data)
	return nil
}

func (t *structureTab) Handle(a keymap.Action) tea.Cmd {
	v := t.view
	switch a {
	case keymap.Down:
		v.Scroll(1)
	case keymap.Up:
		v.Scroll(-1)
	case keymap.Top:
		v.Top()
	case keymap.Bottom:
		v.Bottom()
	case keymap.HalfPageDown:
		v.HalfPage(1)
	case keymap.HalfPageUp:
		v.HalfPage(-1)
	case keymap.NextSection:
		v.NextSection()
	case keymap.PrevSection:
		v.PrevSection()
	case keymap.Refresh:
		if !t.loading {
			return t.load()
		}
	}
	return nil
}

func (t *structureTab) View(bool) string {
	switch {
	case t.err != "":
		return theme.Error.Render("✗ " + textutil.Sanitize(t.err))
	case t.loading:
		return theme.Muted.Render("loading…")
	}
	return t.view.View()
}

// passwordRetrier is a tab that failed for want of a password and can try
// again once it is entered.
type passwordRetrier interface {
	retryAfterPassword() tea.Cmd
}

func (t *dataTab) retryAfterPassword() tea.Cmd {
	if t.retrySubmit {
		t.retrySubmit = false
		return t.submit()
	}
	return t.Handle(keymap.Refresh)
}

func (t *structureTab) retryAfterPassword() tea.Cmd { return t.Handle(keymap.Refresh) }
