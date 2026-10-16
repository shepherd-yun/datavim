package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/importer"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// importTimeout bounds reading, checking or importing one file.
const importTimeout = 30 * time.Minute

// importTick is how often the progress of a check or an import is redrawn.
const importTick = 200 * time.Millisecond

type importPhase int

const (
	importIdle importPhase = iota
	importChecking
	importRunning
)

// The Import tab's sections.
const (
	secSource = iota
	secMapping
	secPreview
	sectionCount
)

// mapRow is the mapping of one file column.
type mapRow struct {
	picked bool
	target int    // existing table: index in cols, -1 for none
	name   string // new table: the column's name
	typ    string // new table: the column's type
}

// importTab imports a CSV, TSV or XLSX file into a table, or into a new
// table of a database or schema (create).
type importTab struct {
	id      int
	key     string
	keys    *keymap.Keymap
	backend Backend
	home    string
	conn    string
	driver  string
	ref     db.TableRef // the table; when create, ref.Name is the new table's name
	create  bool
	path    string
	opts    importer.Options
	null    string
	tick    time.Duration

	gen     int
	loading bool
	cancel  context.CancelFunc
	loadErr string
	sample  importer.Sample
	cols    []db.Column        // the table's columns
	infer   *importer.Inferrer // create: what the file's values look like
	rows    []mapRow

	phase    importPhase
	progress *atomic.Int64
	total    int
	problems []*importer.RowError
	failed   int
	job      *importer.Job // checked, waiting for the confirmation

	section       int
	cursor        [sectionCount]int
	offset        [sectionCount]int
	width, height int
	status        string
	statusErr     bool
}

func importTabKey(conn string, ref db.TableRef, path string) string {
	return "import\x00" + tableKey(conn, ref) + "\x00" + path
}

func newImportTab(id int, b Backend, home, conn, driver string, ref db.TableRef, create bool, path string) *importTab {
	return &importTab{
		id: id, key: importTabKey(conn, ref, path), keys: keymap.Default(), backend: b, home: home,
		conn: conn, driver: driver, ref: ref, create: create, path: path,
		opts: importer.DefaultOptions(path), tick: importTick, section: secMapping,
	}
}

func (t *importTab) ID() int                       { return t.id }
func (t *importTab) Title() string                 { return "import:" + t.ref.Name }
func (t *importTab) Key() string                   { return t.key }
func (t *importTab) Context() keymap.Context       { return keymap.Import }
func (t *importTab) Target() (string, db.TableRef) { return t.conn, t.ref }
func (t *importTab) Init() tea.Cmd                 { return t.load() }
func (t *importTab) SetSize(w, h int)              { t.width, t.height = max(w, 0), max(h, 0) }

func (t *importTab) setStatus(s string, isErr bool) { t.status, t.statusErr = s, isErr }

// refName is the table as people write it: app.users.
func (t *importTab) refName() string { return strings.Join(tableParts(t.driver, t.ref), ".") }

type importLoadedMsg struct {
	tabID, gen int
	sample     importer.Sample
	cols       []db.Column
	infer      *importer.Inferrer
	err        error
}

func (m importLoadedMsg) TabID() int { return m.tabID }

// importEditMsg carries an edit made in a popup back to the tab.
type importEditMsg struct {
	tabID int
	apply func(t *importTab) tea.Cmd
}

func (m importEditMsg) TabID() int { return m.tabID }

// load reads the file's sample (and, for a new table, every row for type
// inference) and the table's columns; a load still running is cancelled.
func (t *importTab) load() tea.Cmd {
	if t.cancel != nil {
		t.cancel()
	}
	t.gen++
	t.loading, t.loadErr, t.problems, t.failed = true, "", nil, 0
	ctx, cancel := context.WithTimeout(context.Background(), importTimeout)
	t.cancel = cancel
	id, gen, b, conn, ref, path, o := t.id, t.gen, t.backend, t.conn, t.ref, t.path, t.opts
	create, driver, null := t.create, t.driver, t.null
	return func() tea.Msg {
		defer cancel()
		msg := importLoadedMsg{tabID: id, gen: gen}
		var scan func([]string)
		if create {
			msg.infer = importer.NewInferrer(driver, null)
			scan = msg.infer.Add
		}
		msg.sample, msg.err = importer.Read(ctx, path, o, importer.PreviewRows, scan)
		if msg.err == nil && !create {
			msg.err = withConn(ctx, b, conn, ref.Database, func(c db.Conn) error {
				var err error
				msg.cols, err = c.Meta().Columns(ctx, ref)
				return err
			})
		}
		return msg
	}
}

func (t *importTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case importLoadedMsg:
		t.loaded(m)
	case importEditMsg:
		return m.apply(t)
	}
	return nil
}

func (t *importTab) loaded(m importLoadedMsg) {
	if m.gen != t.gen {
		return
	}
	t.loading, t.cancel = false, nil
	if m.err != nil {
		t.loadErr, t.sample, t.rows = errorText(m.err), importer.Sample{}, nil
		return
	}
	sameHeader := slices.Equal(t.sample.Header, m.sample.Header) && len(t.rows) == len(m.sample.Header)
	t.sample, t.cols, t.infer = m.sample, m.cols, m.infer
	if sameHeader {
		return
	}
	if t.create {
		t.mapNew()
	} else {
		t.mapByName()
	}
	// The Source cursor stays on the setting just changed.
	for _, s := range []int{secMapping, secPreview} {
		t.cursor[s], t.offset[s] = 0, 0
	}
}

// mapByName maps file columns to table columns of the same name (ignoring
// case and _ - space); the rest are skipped. Generated columns are never
// targets.
func (t *importTab) mapByName() {
	t.rows = make([]mapRow, len(t.sample.Header))
	used := map[int]bool{}
	for i, h := range t.sample.Header {
		t.rows[i].target = -1
		for j, c := range t.cols {
			if c.Generated == "" && !used[j] && importer.MatchKey(c.Name) == importer.MatchKey(h) {
				t.rows[i], used[j] = mapRow{picked: true, target: j}, true
				break
			}
		}
	}
}

// mapNew picks every file column for the new table, named after the header
// and typed by inference.
func (t *importTab) mapNew() {
	names := importer.UniqueIdents(t.sample.Header)
	schema := t.infer.Schema(t.ref, names)
	t.rows = make([]mapRow, len(names))
	for i, n := range names {
		t.rows[i] = mapRow{picked: true, target: -1, name: n, typ: schema.Columns[i].Type}
	}
}

// columns is the picked columns with their targets, in file order.
func (t *importTab) columns() []importer.Column {
	var out []importer.Column
	for i, r := range t.rows {
		if !r.picked {
			continue
		}
		var c db.Column
		switch {
		case t.create:
			c = db.Column{Name: r.name, Type: r.typ, Nullable: t.newNullable(i)}
		case r.target >= 0 && r.target < len(t.cols):
			c = t.cols[r.target]
		default:
			continue
		}
		out = append(out, importer.Column{Source: i, Header: t.sample.Header[i], Target: importer.TargetOf(t.driver, c)})
	}
	return out
}

// newNullable says whether column i of a new table allows NULL: when some
// row had no value for it.
func (t *importTab) newNullable(i int) bool {
	if t.infer == nil {
		return true
	}
	return t.infer.Schema(t.ref, make([]string, len(t.rows))).Columns[i].Nullable
}

// missing lists the table's columns that need a value and get none: NOT
// NULL, no default, not auto-increment, not generated, not mapped.
func (t *importTab) missing() []string {
	if t.create {
		return nil
	}
	used := map[int]bool{}
	for _, r := range t.rows {
		if r.picked && r.target >= 0 {
			used[r.target] = true
		}
	}
	var out []string
	for j, c := range t.cols {
		if !used[j] && !c.Nullable && c.Default == nil && !c.AutoIncrement && c.Generated == "" {
			out = append(out, c.Name)
		}
	}
	return out
}

// importSetting is a row of the Source section.
type importSetting int

const (
	setFile importSetting = iota
	setFormat
	setDelimiter
	setEncoding
	setSheet
	setHeader
	setNull
	setTable
)

func (t *importTab) settings() []importSetting {
	out := []importSetting{setFile, setFormat}
	switch t.opts.Format {
	case importer.CSV:
		out = append(out, setDelimiter, setEncoding)
	case importer.TSV:
		out = append(out, setEncoding)
	case importer.XLSX:
		out = append(out, setSheet)
	}
	out = append(out, setHeader, setNull)
	if t.create {
		out = append(out, setTable)
	}
	return out
}

func (t *importTab) settingText(s importSetting) string {
	var name, value string
	switch s {
	case setFile:
		name, value = "file", tildePath(t.path)
	case setFormat:
		name, value = "format", strings.ToUpper(string(t.opts.Format))
	case setDelimiter:
		name, value = "delimiter", string(t.opts.Delimiter)
	case setEncoding:
		name, value = "encoding", string(t.opts.Encoding)
	case setSheet:
		name, value = "sheet", t.opts.Sheet
		if value == "" {
			value = "(first)"
		}
	case setHeader:
		name, value = "header", fmt.Sprintf("row %d", t.opts.HeaderRow)
		if t.opts.HeaderRow == 0 {
			value = "none"
		}
	case setNull:
		name, value = "null", fmt.Sprintf("%q", t.null)
	case setTable:
		name, value = "table", t.refName()
	}
	return fmt.Sprintf("%-10s %s", name, value)
}

// sectionLen is how many rows the cursor can be on in section s.
func (t *importTab) sectionLen(s int) int {
	switch s {
	case secSource:
		return len(t.settings())
	case secMapping:
		return len(t.rows)
	}
	if t.problems != nil {
		return len(t.problems)
	}
	return len(t.sample.Rows)
}

func (t *importTab) header() string {
	title := "Import → " + t.refName()
	if t.create {
		title = "New table → " + t.refName()
	}
	head := theme.Title.Render(textutil.Sanitize(title))
	if cfg, _ := t.backend.Connection(t.conn); cfg.Env == config.EnvProd {
		head += " " + theme.Prod.Render("[PROD]")
	}
	return head + "   " + theme.Muted.Render(textutil.Sanitize(filepath.Base(t.path)))
}

func (t *importTab) sectionTitle(s int, title string) string {
	if s == t.section {
		return theme.Accent.Render("▌" + title)
	}
	return theme.Muted.Render(" " + title)
}

// window cuts lines to h, scrolling section s so its cursor shows.
func (t *importTab) window(s int, lines []string, h int) []string {
	if h <= 0 || len(lines) == 0 {
		return nil
	}
	cur := textutil.Clamp(t.cursor[s], 0, len(lines)-1)
	t.cursor[s] = cur
	off := t.offset[s]
	if cur < off {
		off = cur
	}
	if cur >= off+h {
		off = cur - h + 1
	}
	off = textutil.Clamp(off, 0, max(len(lines)-h, 0))
	t.offset[s] = off
	out := slices.Clone(lines[off:min(off+h, len(lines))])
	for i := range out {
		line := textutil.Fit(out[i], t.width)
		if s == t.section && off+i == cur {
			line = theme.Selected.Render(textutil.Fit(stripStyle(out[i]), t.width))
		}
		out[i] = line
	}
	return out
}

func (t *importTab) sourceLines() []string {
	var out []string
	for _, s := range t.settings() {
		out = append(out, "  "+textutil.Sanitize(t.settingText(s)))
	}
	return out
}

func (t *importTab) mappingLines() []string {
	width := 0
	for _, h := range t.sample.Header {
		width = max(width, min(24, len([]rune(h))))
	}
	var out []string
	for i, r := range t.rows {
		box := "[ ]"
		if r.picked {
			box = "[✓]"
		}
		target := "(skip)"
		switch {
		case t.create:
			target = r.name + "  " + r.typ
		case r.target >= 0:
			c := t.cols[r.target]
			target = c.Name + "  " + c.Type
		}
		h := []rune(t.sample.Header[i])
		if len(h) > 24 {
			h = append(h[:23], '…')
		}
		out = append(out, textutil.Sanitize(fmt.Sprintf("  %s %-*s  → %s", box, width, string(h), target)))
	}
	return out
}

// previewLines is the first rows as they will be inserted, under a header
// and a rule; a row with a cell that cannot be converted shows ✗ and the
// cell's text, in red.
func (t *importTab) previewLines() (head, rows []string) {
	cols := t.columns()
	if len(cols) == 0 {
		return []string{theme.Muted.Render("  nothing picked")}, nil
	}
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = clipCell(c.Target.Name)
	}
	cells := make([][]string, len(t.sample.Rows))
	bad := make([]bool, len(t.sample.Rows))
	for r, row := range t.sample.Rows {
		cells[r] = make([]string, len(cols))
		for i, c := range cols {
			text := ""
			if c.Source < len(row) {
				text = row[c.Source]
			}
			v, err := importer.Convert(c.Target, text, t.null)
			switch {
			case err != nil:
				cells[r][i], bad[r] = "✗ "+clipCell(text), true
			case v.Null:
				cells[r][i] = "NULL"
			case v.Default:
				cells[r][i] = "DEFAULT"
			default:
				cells[r][i] = clipCell(v.Text)
			}
		}
	}
	lines := textutil.Table(headers, cells)
	head = []string{"  " + theme.Header.Render(lines[0]), "  " + theme.Muted.Render(lines[1])}
	if len(t.sample.Rows) == 0 {
		return append(head, theme.Muted.Render("  the file has no rows")), nil
	}
	for r, l := range lines[2:] {
		if bad[r] {
			l = theme.Error.Render(l)
		}
		rows = append(rows, "  "+l)
	}
	return head, rows
}

// previewError is why preview row i cannot be imported ("" when it can).
func (t *importTab) previewError(i int) string {
	if i < 0 || i >= len(t.sample.Rows) {
		return ""
	}
	p := importer.Plan{Columns: t.columns(), Width: len(t.sample.Header), Null: t.null}
	if _, _, err := p.Row(t.sample.Lines[i], t.sample.Rows[i]); err != nil {
		return err.Error()
	}
	return ""
}

func clipCell(s string) string {
	s = strings.ReplaceAll(s, "\n", "⏎")
	if r := []rune(s); len(r) > 24 {
		return string(r[:23]) + "…"
	}
	return s
}

func (t *importTab) problemLines() []string {
	out := make([]string, 0, len(t.problems)+1)
	for _, p := range t.problems {
		out = append(out, "  "+theme.Error.Render(textutil.Sanitize(p.Error())))
	}
	if more := t.failed - len(t.problems); more > 0 {
		out = append(out, theme.Muted.Render(fmt.Sprintf("  … and %s more", plural(more, "row"))))
	}
	return out
}

func (t *importTab) View(bool) string {
	lines := []string{textutil.Fit(t.header(), t.width)}
	avail := max(t.height-2, 3)
	src := t.sourceLines()
	lines = append(lines, t.sectionTitle(secSource, "Source"))
	lines = append(lines, t.window(secSource, src, len(src))...)
	if t.loadErr != "" {
		lines = append(lines, theme.Error.Render(textutil.Fit("  ✗ "+textutil.Sanitize(t.loadErr), t.width)))
	} else if t.sample.BadUTF8 && t.opts.Encoding == importer.UTF8 {
		lines = append(lines, theme.Error.Render(textutil.Fit("  the file is not UTF-8 · try encoding GBK", t.width)))
	}
	rest := avail - len(lines)
	mapH := max(min(len(t.rows), (rest-4)/2), 1)
	lines = append(lines, t.sectionTitle(secMapping, "Mapping"))
	switch {
	case t.loading:
		lines = append(lines, theme.Muted.Render("  reading the file…"))
	default:
		lines = append(lines, t.window(secMapping, t.mappingLines(), mapH)...)
	}
	if miss := t.missing(); len(miss) > 0 {
		lines = append(lines, theme.Error.Render(textutil.Fit("  "+t.refName()+" needs values for "+strings.Join(miss, ", "), t.width)))
	}
	title, head, body := "Preview", []string(nil), []string(nil)
	if t.problems != nil {
		title, body = fmt.Sprintf("Problems (%s)", plural(t.failed, "row")), t.problemLines()
	} else {
		head, body = t.previewLines()
	}
	lines = append(lines, t.sectionTitle(secPreview, title))
	if !t.loading && t.loadErr == "" {
		for _, h := range head {
			lines = append(lines, textutil.Fit(h, t.width))
		}
		lines = append(lines, t.window(secPreview, body, max(avail-len(lines), 1))...)
	}
	for len(lines) < t.height-1 {
		lines = append(lines, "")
	}
	lines = lines[:max(t.height-1, 1)]
	return strings.Join(append(lines, textutil.Fit(t.statusLine(), t.width)), "\n")
}

func (t *importTab) statusLine() string {
	switch {
	case t.status != "" && t.statusErr:
		return theme.Error.Render(t.status)
	case t.status != "":
		return t.status
	case t.phase != importIdle:
		return theme.Muted.Render(joinHints(t.progressText(), keyHint(t.keys, keymap.Global, keymap.Cancel, "cancels")))
	case t.section == secPreview && t.problems == nil:
		if e := t.previewError(t.cursor[secPreview]); e != "" {
			return theme.Error.Render(textutil.Sanitize(e))
		}
	}
	return theme.Muted.Render(joinHints(
		keyHint(t.keys, keymap.Import, keymap.NextSection, "next section"),
		keyHint(t.keys, keymap.Import, keymap.ImportToggle, "import/skip"),
		keyHint(t.keys, keymap.Import, keymap.EditCell, "edit"),
		keyHint(t.keys, keymap.Import, keymap.SubmitEdits, "import"),
	))
}

// progressText is set up in Task 11; until then it is empty.
func (t *importTab) progressText() string { return "" }

// stripStyle drops ANSI styling, so a selected line is drawn in one style.
func stripStyle(s string) string { return ansi.Strip(s) }

func (t *importTab) Handle(keymap.Action) tea.Cmd { return nil }
