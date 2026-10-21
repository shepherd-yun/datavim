package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/export"
	"github.com/shepherd-yun/datavim/internal/sqlx"
	"github.com/shepherd-yun/datavim/internal/ui/clipboard"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
)

// copyToClipboard and readClipboard are replaced in tests.
var (
	copyToClipboard = clipboard.Copy
	readClipboard   = clipboard.Read
)

// copy puts text on the clipboard and in the editors' unnamed register,
// so that p in a console puts what y copied anywhere.
func (m *Model) copy(text string) tea.Cmd {
	m.regs.Put(text)
	return copyToClipboard(text)
}

const exportTimeout = 10 * time.Minute

// exportSrc is what a tab offers for copy and export.
type exportSrc struct {
	rs    *db.ResultSet
	name  string // file name base
	opts  export.Options
	conn  string
	ref   db.TableRef
	query tableQuery // data tabs: the filter and order of a whole-table export
	whole bool       // the whole table can be streamed (data tabs)

	// stream writes the whole source, page by page; nil when there is none
	// beyond rs. It counts the rows and the cells it wrote escaped.
	stream     func(ctx context.Context, w export.Writer, rows, escaped *int) error
	wholeLabel string // the scope choice's second option, e.g. "Whole key (12,345 fields)"
	escaped    int    // cells of rs written escaped, for the status
	note       string // added to the status, e.g. " · includes unsaved changes"
	// jsonl, when set, is what JSON Lines exports instead of rs (Kafka
	// messages in full rather than their previews).
	jsonl *db.ResultSet
}

// yanker is a tab whose yy / yl copy something else than the grid's text.
type yanker interface {
	yankText(a keymap.Action) (text, what string, ok bool)
}

type (
	exporter interface{ exportSource() exportSrc }
	gridder  interface{ activeGrid() *grid.Model }
)

type exportScopeMsg struct {
	tabID  int
	format export.Format
	whole  bool
}

type exportPathMsg struct {
	tabID  int
	format export.Format
	whole  bool
	path   string
}

type exportDoneMsg struct {
	path    string
	conn    string
	rows    int
	escaped int    // binary cells written escaped
	note    string // exportSrc.note
	err     error
}

// tableParts is the INSERT target: database.table on engines without
// schemas (MySQL), else schema.table.
func tableParts(d sqlx.Dialect, ref db.TableRef) []string {
	ns := ref.Database
	if d.Features().Schemas {
		ns = ref.Schema
	}
	if ns == "" {
		return []string{ref.Name}
	}
	return []string{ns, ref.Name}
}

func (t *dataTab) exportSource() exportSrc {
	cfg, _ := t.backend.Connection(t.conn)
	q := t.query
	q.rowID = false
	return exportSrc{
		rs: t.grid.Result(), name: t.ref.Name, conn: t.conn, ref: t.ref, query: q, whole: true,
		opts: export.Options{Driver: cfg.Driver, Table: tableParts(sqlx.DialectOf(cfg.Driver), t.ref)},
	}
}

func (t *dataTab) activeGrid() *grid.Model { return t.grid }

func (t *consoleTab) exportSource() exportSrc {
	cfg, _ := t.backend.Connection(t.conn)
	src := exportSrc{rs: t.grid.Result(), name: "query", conn: t.conn, opts: export.Options{Driver: cfg.Driver}}
	if t.last != nil {
		if target := sqlx.Analyze(t.last.SQL, t.dialect).Target; target != "" {
			src.name, src.opts.Table = target, []string{target}
		}
	}
	return src
}

func (t *consoleTab) activeGrid() *grid.Model { return t.grid }

func expandHome(p, home string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	}
	return p
}

func (m *Model) source() (exportSrc, bool) {
	e, ok := m.activeTab().(exporter)
	if !ok || m.focus != focusTabs {
		return exportSrc{}, false
	}
	src := e.exportSource()
	return src, src.rs != nil && len(src.rs.Columns) > 0
}

func (m *Model) copyAs(f export.Format) tea.Cmd {
	src, ok := m.source()
	if !ok {
		m.setStatus("nothing to copy", true)
		return nil
	}
	text, err := export.Encode(f, src.rs, src.opts)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	m.setStatus(fmt.Sprintf("copied %d rows as %s", len(src.rs.Rows), f.Label()), false)
	return m.copy(text)
}

func (m *Model) yank(a keymap.Action) tea.Cmd {
	if y, ok := m.activeTab().(yanker); ok {
		if text, what, ok := y.yankText(a); ok {
			m.setStatus("copied "+what, false)
			return m.copy(text)
		}
	}
	if k, ok := m.activeTab().(*keyTab); ok && k.isString() && a == keymap.YankCell {
		m.setStatus("copied "+k.Title(), false)
		return m.copy(k.text) // what was read of it, unescaped
	}
	g, ok := m.activeTab().(gridder)
	if !ok {
		return nil
	}
	gr := g.activeGrid()
	if a == keymap.YankCell {
		col, v, ok := gr.Cell()
		if !ok {
			m.setStatus("nothing to copy", true)
			return nil
		}
		text := v.Text
		if v.Null {
			text = "NULL"
		}
		m.setStatus("copied "+col.Name, false)
		return m.copy(text)
	}
	row, ok := gr.Row()
	if !ok {
		m.setStatus("nothing to copy", true)
		return nil
	}
	one := &db.ResultSet{Columns: gr.Result().Columns, Rows: [][]db.Value{row}}
	text, err := export.Encode(export.TSV, one, export.Options{NoHeader: true})
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	m.setStatus("copied row", false)
	return m.copy(text)
}

func (m *Model) exportAs(f export.Format) tea.Cmd {
	src, ok := m.source()
	if !ok {
		m.setStatus("nothing to export", true)
		return nil
	}
	id := m.activeTab().ID()
	if src.whole {
		whole := "Whole table"
		if src.query.where != "" {
			whole = "Every row matching the filter"
		}
		m.overlay = overlay.NewChoice("Export "+f.Label(),
			[]string{fmt.Sprintf("Current page (%d rows)", len(src.rs.Rows)), whole},
			func(i int) tea.Msg { return exportScopeMsg{tabID: id, format: f, whole: i == 1} })
		return nil
	}
	if src.stream != nil {
		m.overlay = overlay.NewChoice("Export "+f.Label(),
			[]string{fmt.Sprintf("Loaded rows (%d)", len(src.rs.Rows)), src.wholeLabel},
			func(i int) tea.Msg { return exportScopeMsg{tabID: id, format: f, whole: i == 1} })
		return nil
	}

	m.askExportPath(id, f, false, src.name)
	return nil
}

func (m *Model) askExportPath(tabID int, f export.Format, whole bool, name string) {
	path := filepath.Join(m.settings.ExportDirectory(m.home), export.FileName(name, f, time.Now()))
	m.overlay = overlay.NewPrompt("Export "+f.Label(), "Save to:", path, func(p string) tea.Msg {
		return exportPathMsg{tabID: tabID, format: f, whole: whole, path: p}
	})
}

func (m *Model) exportScope(msg exportScopeMsg) {
	if e, ok := m.tabByID(msg.tabID).(exporter); ok {
		m.askExportPath(msg.tabID, msg.format, msg.whole, e.exportSource().name)
	}
}

func (m *Model) exportTo(msg exportPathMsg) tea.Cmd {
	e, ok := m.tabByID(msg.tabID).(exporter)
	if !ok {
		m.setStatus("the tab was closed", true)
		return nil
	}
	path := expandHome(strings.TrimSpace(msg.path), m.home)
	m.setStatus("exporting to "+path+"…", false)
	return exportCmd(m.backend, e.exportSource(), msg.format, msg.whole, path)
}

func exportCmd(b Backend, src exportSrc, f export.Format, whole bool, path string) tea.Cmd {
	return func() tea.Msg {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return exportDoneMsg{path: path, err: err}
		}
		if !whole {
			rs := src.rs
			if f == export.JSONL && src.jsonl != nil {
				rs = src.jsonl
			}
			err := export.ToFile(path, f, src.opts, func(w export.Writer) error { return export.WriteRows(w, rs) })
			return exportDoneMsg{path: path, rows: len(rs.Rows), escaped: src.escaped, note: src.note, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
		defer cancel()
		rows := 0
		if src.stream != nil {
			escaped := 0
			err := export.ToFile(path, f, src.opts, func(w export.Writer) error { return src.stream(ctx, w, &rows, &escaped) })
			return exportDoneMsg{path: path, conn: src.conn, rows: rows, escaped: escaped, err: err}
		}
		err := withConn(ctx, b, src.conn, src.ref.Database, func(c db.Conn) error {
			return streamTable(ctx, c, src, f, path, &rows)
		})
		return exportDoneMsg{path: path, conn: src.conn, rows: rows, err: err}
	}
}

// streamTable writes the whole table to path without holding it in memory.
func streamTable(ctx context.Context, c db.Conn, src exportSrc, f export.Format, path string, rows *int) error {
	*rows = 0
	return export.ToFile(path, f, src.opts, func(w export.Writer) error {
		var headerErr error
		err := c.Stream(ctx, src.query.sql(sqlx.DialectOf(src.opts.Driver), src.ref),
			func(cols []db.ColumnInfo) { headerErr = w.WriteHeader(cols) },
			func(row []db.Value) error {
				if headerErr != nil {
					return headerErr
				}
				*rows++
				return w.WriteRow(row)
			})
		return errors.Join(err, headerErr)
	})
}

func (m *Model) exportDone(msg exportDoneMsg) {
	if msg.err != nil {
		m.setStatus("export failed: "+msg.err.Error(), true)
		return
	}
	s := fmt.Sprintf("exported %d rows to %s", msg.rows, msg.path) + msg.note
	if msg.escaped > 0 {
		s += fmt.Sprintf(` · %d binary values written escaped (\xNN)`, msg.escaped)
	}
	m.setStatus(s, false)
}
