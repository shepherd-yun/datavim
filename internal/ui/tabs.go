package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
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
	err     error
}

func (m pageLoadedMsg) TabID() int { return m.tabID }

// filterMsg is a filter typed at the F prompt.
type filterMsg struct {
	tabID int
	text  string
}

func (m filterMsg) TabID() int { return m.tabID }

type dataTab struct {
	id       int
	backend  Backend
	conn     string
	ref      db.TableRef
	dialect  sqlx.Dialect
	pageSize int
	grid     *grid.Model

	page       int
	hasMore    bool
	loading    bool
	err        string
	status     string // what the last key did; cleared by the next one
	statusErr  bool
	query      tableQuery
	known      bool // query.key has been read from the catalog
	height     int
	wantColumn string // column to put the cursor on once data is shown
}

func newDataTab(id int, b Backend, conn string, ref db.TableRef, s config.Settings) *dataTab {
	cfg, _ := b.Connection(conn)
	return &dataTab{
		id: id, backend: b, conn: conn, ref: ref, dialect: sqlx.DialectOf(cfg.Driver), pageSize: s.PageSize,
		grid: grid.New(nil, s.MaxColumnWidth),
	}
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
				cols, err := c.Meta().Columns(ctx, ref)
				if err != nil {
					return err
				}
				msg.query.key = primaryKey(cols)
			}
			var err error
			msg.rs, msg.hasMore, err = fetchPage(ctx, c, d, ref, msg.query, page, size)
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

func fetchPage(ctx context.Context, c db.Conn, d sqlx.Dialect, ref db.TableRef, q tableQuery, page, size int) (*db.ResultSet, bool, error) {
	rs, err := c.Query(ctx, c.Dialect().Paginate(q.sql(d, ref), size+1, page*size))
	if err != nil {
		return nil, false, err
	}
	hasMore := len(rs.Rows) > size
	if hasMore {
		rs.Rows = rs.Rows[:size]
	}
	return rs, hasMore, nil
}

func (t *dataTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case pageLoadedMsg:
		t.pageLoaded(m)
	case filterMsg:
		where := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(m.text), ";"))
		return t.applyQuery(func(q *tableQuery) { q.where = where })
	}
	return nil
}

func (t *dataTab) pageLoaded(m pageLoadedMsg) {
	t.loading = false
	if m.err != nil {
		t.err = m.err.Error()
		return
	}
	t.err, t.page, t.hasMore = "", m.page, m.hasMore
	t.query, t.known = m.query, true
	t.grid.SetSort(t.query.sort, t.query.desc)
	t.grid.SetResult(m.rs)
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
	case keymap.FilterValue:
		col, v, ok := t.grid.Cell()
		if !ok {
			t.setStatus("no cell to filter by", true)
			return nil
		}
		cond := valueFilter(t.dialect, col, v)
		return t.applyQuery(func(q *tableQuery) { q.where = andFilter(q.where, cond) })
	case keymap.FilterPrompt:
		id := t.id
		return openOverlay(overlay.NewPrompt("Filter "+t.ref.Name, "WHERE … (empty shows every row)", t.query.where,
			func(s string) tea.Msg { return filterMsg{tabID: id, text: s} }))
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
	return theme.Muted.Render(textutil.Sanitize(t.pageInfo()))
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

func (t *dataTab) retryAfterPassword() tea.Cmd      { return t.Handle(keymap.Refresh) }
func (t *structureTab) retryAfterPassword() tea.Cmd { return t.Handle(keymap.Refresh) }
