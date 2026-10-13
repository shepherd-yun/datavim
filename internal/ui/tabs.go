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
	rs      *db.ResultSet
	hasMore bool
	orderBy []string
	err     error
}

func (m pageLoadedMsg) TabID() int { return m.tabID }

type dataTab struct {
	id       int
	backend  Backend
	conn     string
	ref      db.TableRef
	pageSize int
	grid     *grid.Model

	page       int
	hasMore    bool
	loading    bool
	err        string
	orderBy    []string
	orderKnown bool
	height     int
}

func newDataTab(id int, b Backend, conn string, ref db.TableRef, s config.Settings) *dataTab {
	return &dataTab{
		id: id, backend: b, conn: conn, ref: ref, pageSize: s.PageSize,
		grid: grid.New(nil, s.MaxColumnWidth),
	}
}

func (t *dataTab) ID() int                       { return t.id }
func (t *dataTab) Title() string                 { return t.ref.Name }
func (t *dataTab) Key() string                   { return dataTabKey(t.conn, t.ref) }
func (t *dataTab) Context() keymap.Context       { return keymap.Grid }
func (t *dataTab) Target() (string, db.TableRef) { return t.conn, t.ref }
func (t *dataTab) Init() tea.Cmd                 { return t.load(0) }

func (t *dataTab) SetSize(w, h int) {
	t.height = max(h, 0)
	t.grid.SetSize(w, max(h-1, 0))
}

func (t *dataTab) load(page int) tea.Cmd {
	t.loading = true
	id, b, conn, ref, size := t.id, t.backend, t.conn, t.ref, t.pageSize
	orderBy, known := t.orderBy, t.orderKnown
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := pageLoadedMsg{tabID: id, conn: conn, page: page, orderBy: orderBy}
		msg.err = withConn(ctx, b, conn, ref.Database, func(c db.Conn) error {
			if !known {
				cols, err := c.Meta().Columns(ctx, ref)
				if err != nil {
					return err
				}
				msg.orderBy = primaryKey(cols)
			}
			var err error
			msg.rs, msg.hasMore, err = fetchPage(ctx, c, ref, msg.orderBy, page, size)
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

// selectAll is the table-data query in primary-key order.
func selectAll(d db.Dialect, ref db.TableRef, orderBy []string) string {
	q := "SELECT * FROM " + d.QualifiedName(ref)
	if len(orderBy) > 0 {
		quoted := make([]string, len(orderBy))
		for i, col := range orderBy {
			quoted[i] = d.QuoteIdent(col)
		}
		q += " ORDER BY " + strings.Join(quoted, ", ")
	}
	return q
}

func fetchPage(ctx context.Context, c db.Conn, ref db.TableRef, orderBy []string, page, size int) (*db.ResultSet, bool, error) {
	d := c.Dialect()
	rs, err := c.Query(ctx, d.Paginate(selectAll(d, ref, orderBy), size+1, page*size))
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
	m, ok := msg.(pageLoadedMsg)
	if !ok {
		return nil
	}
	t.loading = false
	if m.err != nil {
		t.err = m.err.Error()
		return nil
	}
	t.err, t.page, t.hasMore = "", m.page, m.hasMore
	t.orderBy, t.orderKnown = m.orderBy, true
	t.grid.SetResult(m.rs)
	return nil
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
	if cmd, ok := gridAction(t.grid, a); ok {
		return cmd
	}
	switch a {
	case keymap.NextPage:
		if t.hasMore && !t.loading {
			return t.load(t.page + 1)
		}
	case keymap.PrevPage:
		if t.page > 0 && !t.loading {
			return t.load(t.page - 1)
		}
	case keymap.Refresh:
		if !t.loading {
			t.orderKnown = false
			return t.load(t.page)
		}
	}
	return nil
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
	}
	n := t.grid.RowCount()
	if n == 0 {
		return theme.Muted.Render(fmt.Sprintf("no rows · page %d", t.page+1))
	}
	first := t.page*t.pageSize + 1
	s := fmt.Sprintf("rows %d–%d · page %d", first, first+n-1, t.page+1)
	if t.hasMore {
		s += " · ] next"
	}
	if t.page > 0 {
		s += " · [ prev"
	}
	return theme.Muted.Render(s)
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
	return &structureTab{id: id, backend: b, conn: conn, ref: ref, view: structure.New()}
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
