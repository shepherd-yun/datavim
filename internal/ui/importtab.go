package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/importer"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
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
	case importCheckedMsg:
		return t.checked(m)
	case importDoneMsg:
		t.done(m)
	case importTickMsg:
		if m.gen == t.gen && t.phase != importIdle {
			return t.tickCmd()
		}
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

// stripStyle drops ANSI styling, so a selected line is drawn in one style.
func stripStyle(s string) string { return ansi.Strip(s) }

func (t *importTab) Handle(a keymap.Action) tea.Cmd {
	t.setStatus("", false)
	if t.phase != importIdle {
		t.setStatus(joinHints("importing…", keyHint(t.keys, keymap.Global, keymap.Cancel, "cancels")), false)
		return nil
	}
	switch a {
	case keymap.Down:
		t.move(1)
	case keymap.Up:
		t.move(-1)
	case keymap.Top:
		t.cursor[t.section] = 0
	case keymap.Bottom:
		t.move(t.sectionLen(t.section))
	case keymap.HalfPageDown:
		t.move(max(t.height/4, 1))
	case keymap.HalfPageUp:
		t.move(-max(t.height/4, 1))
	case keymap.NextSection:
		t.section = (t.section + 1) % sectionCount
	case keymap.PrevSection:
		t.section = (t.section + sectionCount - 1) % sectionCount
	case keymap.EditCell:
		return t.edit()
	case keymap.ImportToggle:
		t.toggle()
	case keymap.ImportAll:
		t.pickAll(true)
	case keymap.ImportNone:
		t.pickAll(false)
	case keymap.ImportType:
		return t.editType()
	case keymap.Refresh:
		return t.load()
	case keymap.SubmitEdits:
		return t.submit()
	}
	return nil
}

func (t *importTab) move(delta int) {
	t.cursor[t.section] = textutil.Clamp(t.cursor[t.section]+delta, 0, max(t.sectionLen(t.section)-1, 0))
}

// mapCursor is the mapping row under the cursor; outside the mapping it
// says where the mapping keys work.
func (t *importTab) mapCursor() (int, bool) {
	if t.section != secMapping {
		t.setStatus(joinHints("this works in the mapping", keyHint(t.keys, keymap.Import, keymap.NextSection, "moves between sections")), true)
		return 0, false
	}
	if len(t.rows) == 0 {
		return 0, false
	}
	return textutil.Clamp(t.cursor[secMapping], 0, len(t.rows)-1), true
}

func (t *importTab) toggle() {
	i, ok := t.mapCursor()
	if !ok {
		return
	}
	if !t.create && t.rows[i].target < 0 {
		t.setStatus(joinHints("no target column", keyHint(t.keys, keymap.Import, keymap.EditCell, "picks one")), true)
		return
	}
	t.rows[i].picked = !t.rows[i].picked
}

func (t *importTab) pickAll(on bool) {
	if _, ok := t.mapCursor(); !ok {
		return
	}
	for i := range t.rows {
		if t.create || t.rows[i].target >= 0 {
			t.rows[i].picked = on
		}
	}
}

// setTarget maps file column i to table column target (-1: skip). A target
// held by a skipped row moves over; one held by a picked row is refused.
func (t *importTab) setTarget(i, target int) {
	if target < 0 {
		t.rows[i] = mapRow{target: -1}
		return
	}
	for j, r := range t.rows {
		if j == i || r.target != target {
			continue
		}
		if r.picked {
			t.setStatus(t.cols[target].Name+" already takes "+t.sample.Header[j], true)
			return
		}
		t.rows[j].target = -1
	}
	t.rows[i] = mapRow{picked: true, target: target}
}

func (t *importTab) edit() tea.Cmd {
	switch t.section {
	case secSource:
		s := t.settings()
		return t.editSetting(s[textutil.Clamp(t.cursor[secSource], 0, len(s)-1)])
	case secMapping:
		return t.editMapping()
	}
	return nil
}

func nonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("enter a value")
	}
	return nil
}

// ask opens a prompt whose answer set applies to the tab.
func (t *importTab) ask(title, label, initial string, validate func(string) error, set func(t *importTab, v string) tea.Cmd) tea.Cmd {
	id := t.id
	var opts []overlay.PromptOption
	if validate != nil {
		opts = append(opts, overlay.WithValidate(validate))
	}
	return openOverlay(overlay.NewPrompt(title, label, initial, func(v string) tea.Msg {
		return importEditMsg{tabID: id, apply: func(t *importTab) tea.Cmd { return set(t, v) }}
	}, opts...))
}

func next[T comparable](list []T, cur T) T {
	return list[(slices.Index(list, cur)+1)%len(list)]
}

func (t *importTab) editSetting(s importSetting) tea.Cmd {
	switch s {
	case setFile:
		return t.ask("Import", "File:", t.path, nonEmpty, func(t *importTab, v string) tea.Cmd {
			p := expandHome(strings.TrimSpace(v), t.home)
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			t.path, t.opts.Format, t.opts.Sheet = p, importer.DefaultOptions(p).Format, ""
			return t.load()
		})
	case setFormat:
		t.opts.Format, t.opts.Sheet = next([]importer.Format{importer.CSV, importer.TSV, importer.XLSX}, t.opts.Format), ""
		return t.load()
	case setDelimiter:
		t.opts.Delimiter = next([]rune{',', ';', '|'}, t.opts.Delimiter)
		return t.load()
	case setEncoding:
		t.opts.Encoding = next([]importer.Encoding{importer.UTF8, importer.GBK}, t.opts.Encoding)
		return t.load()
	case setHeader:
		return t.ask("Header", "Row with the column names (0: none):", strconv.Itoa(t.opts.HeaderRow), func(v string) error {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err != nil || n < 0 {
				return errors.New("enter a row number, 0 for none")
			}
			return nil
		}, func(t *importTab, v string) tea.Cmd {
			t.opts.HeaderRow, _ = strconv.Atoi(strings.TrimSpace(v))
			return t.load()
		})
	case setSheet:
		id, path := t.id, t.path
		return func() tea.Msg {
			names, err := importer.Sheets(path)
			if err != nil {
				return importEditMsg{tabID: id, apply: func(t *importTab) tea.Cmd { t.setStatus(errorText(err), true); return nil }}
			}
			return openOverlayMsg{overlay: overlay.NewPicker("Sheet", names, func(i int) tea.Msg {
				return importEditMsg{tabID: id, apply: func(t *importTab) tea.Cmd { t.opts.Sheet = names[i]; return t.load() }}
			})}
		}
	case setNull:
		return t.ask("Empty cells", "Text that means NULL:", t.null, nil, func(t *importTab, v string) tea.Cmd {
			t.null = v
			return t.load()
		})
	case setTable:
		return t.ask("New table", "Name:", t.ref.Name, nonEmpty, func(t *importTab, v string) tea.Cmd {
			t.ref.Name = strings.TrimSpace(v)
			return nil
		})
	}
	return nil
}

func (t *importTab) editMapping() tea.Cmd {
	i, ok := t.mapCursor()
	if !ok {
		return nil
	}
	id, header := t.id, t.sample.Header[i]
	if t.create {
		return t.ask("Column "+header, "Name:", t.rows[i].name, nonEmpty, func(t *importTab, v string) tea.Cmd {
			t.rows[i].name = strings.TrimSpace(v)
			return nil
		})
	}
	labels, idx := []string{"(skip)"}, []int{-1}
	for j, c := range t.cols {
		if c.Generated == "" {
			labels, idx = append(labels, c.Name+"  "+c.Type), append(idx, j)
		}
	}
	return openOverlay(overlay.NewPicker(header+" →", labels, func(k int) tea.Msg {
		return importEditMsg{tabID: id, apply: func(t *importTab) tea.Cmd { t.setTarget(i, idx[k]); return nil }}
	}))
}

func (t *importTab) editType() tea.Cmd {
	if !t.create {
		t.setStatus(joinHints("the table's types are fixed", keyHint(t.keys, keymap.Import, keymap.ImportType, "changes types of a new table")), true)
		return nil
	}
	i, ok := t.mapCursor()
	if !ok {
		return nil
	}
	return t.ask("Column "+t.rows[i].name, "Type:", t.rows[i].typ, nonEmpty, func(t *importTab, v string) tea.Cmd {
		t.rows[i].typ = strings.TrimSpace(v)
		return nil
	})
}

type (
	importCheckedMsg struct {
		tabID, gen int
		job        importer.Job
		result     importer.CheckResult
		exists     bool
		err        error
	}
	// importConfirmedMsg is the user's yes; the root model checks again
	// that the import may run, then starts it.
	importConfirmedMsg struct{ tabID int }
	importDoneMsg      struct {
		tabID  int
		conn   string
		ref    db.TableRef
		create bool
		rows   int
		took   time.Duration
		err    error
	}
	importTickMsg struct{ tabID, gen int }
)

func (m importCheckedMsg) TabID() int { return m.tabID }
func (m importDoneMsg) TabID() int    { return m.tabID }
func (m importTickMsg) TabID() int    { return m.tabID }

// plan is the import as mapped, or why it cannot run.
func (t *importTab) plan() (importer.Plan, string) {
	p := importer.Plan{Driver: t.driver, Table: t.ref, Columns: t.columns(), Width: len(t.sample.Header), Null: t.null}
	switch {
	case t.loading:
		return p, "the file is still loading"
	case t.loadErr != "":
		return p, t.loadErr
	case len(p.Columns) == 0:
		return p, "pick at least one column"
	case len(t.missing()) > 0:
		return p, t.refName() + " needs values for " + strings.Join(t.missing(), ", ")
	case t.create && strings.TrimSpace(t.ref.Name) == "":
		return p, "name the new table"
	}
	if t.create {
		seen := map[string]bool{}
		for _, c := range p.Columns {
			k := strings.ToLower(c.Target.Name)
			if k == "" || seen[k] {
				return p, "two columns are called " + c.Target.Name
			}
			seen[k] = true
		}
	}
	return p, ""
}

// newSchema is the new table: the picked columns, named and typed as mapped.
func (t *importTab) newSchema() db.TableSchema {
	s := db.TableSchema{Ref: t.ref}
	for _, c := range t.columns() {
		s.Columns = append(s.Columns, db.Column{
			Name: c.Target.Name, Type: c.Target.Type, Nullable: c.Target.Nullable, Position: len(s.Columns) + 1,
		})
	}
	return s
}

func (t *importTab) tickCmd() tea.Cmd {
	id, gen := t.id, t.gen
	return tea.Tick(t.tick, func(time.Time) tea.Msg { return importTickMsg{tabID: id, gen: gen} })
}

// submit checks the whole file (and, for a new table, that the name is
// free), then asks for confirmation.
func (t *importTab) submit() tea.Cmd {
	p, problem := t.plan()
	if problem != "" {
		t.setStatus(problem, true)
		return nil
	}
	if t.cancel != nil {
		t.cancel()
	}
	t.gen++
	t.phase, t.problems, t.failed, t.job = importChecking, nil, 0, nil
	t.progress = new(atomic.Int64)
	ctx, cancel := context.WithTimeout(context.Background(), importTimeout)
	t.cancel = cancel
	id, gen, b, conn, ref, path, o, progress := t.id, t.gen, t.backend, t.conn, t.ref, t.path, t.opts, t.progress
	create, schema := t.create, t.newSchema()
	check := func() tea.Msg {
		defer cancel()
		msg := importCheckedMsg{tabID: id, gen: gen, job: importer.Job{Path: path, Options: o, Plan: p}}
		if create {
			msg.err = withConn(ctx, b, conn, ref.Database, func(c db.Conn) error {
				tables, err := c.Meta().Tables(ctx, ref.Namespace)
				if err != nil {
					return err
				}
				msg.exists = slices.ContainsFunc(tables, func(x db.Table) bool { return strings.EqualFold(x.Name, ref.Name) })
				w := c.Dialect().DDL()
				msg.job.Create, _ = w.CreateTable(schema)
				msg.job.Drop = w.DropTable(ref, false)
				return nil
			})
			if msg.err != nil || msg.exists {
				return msg
			}
		}
		msg.result, msg.err = importer.Check(ctx, path, o, p, progress)
		return msg
	}
	return tea.Batch(check, t.tickCmd())
}

func (t *importTab) checked(m importCheckedMsg) tea.Cmd {
	if m.gen != t.gen {
		return nil
	}
	t.phase, t.cancel = importIdle, nil
	switch {
	case errors.Is(m.err, context.Canceled):
		t.setStatus("check cancelled", false)
	case m.err != nil:
		t.setStatus("check failed: "+errorText(m.err), true)
	case m.exists:
		t.setStatus(joinHints("table "+t.refName()+" already exists", "rename it in the Source section"), true)
	case m.result.Failed > 0:
		t.problems, t.failed = m.result.Errors, m.result.Failed
		t.section, t.cursor[secPreview], t.offset[secPreview] = secPreview, 0, 0
		t.setStatus(plural(m.result.Failed, "row")+" cannot be imported · nothing was written", true)
	case m.result.Rows == 0:
		t.setStatus("the file has no rows to import", true)
	default:
		t.total = m.result.Rows
		job := m.job
		t.job = &job
		return openOverlay(t.confirmation())
	}
	return nil
}

func (t *importTab) confirmation() overlay.Overlay {
	cfg, _ := t.backend.Connection(t.conn)
	title := "Import into " + t.refName()
	if t.create {
		title = "Create " + t.refName()
	}
	var lines []string
	for _, q := range t.job.Create {
		lines = append(lines, q+";", "")
	}
	insert := len(lines)
	lines = append(lines, fmt.Sprintf("Insert %s into %s in one transaction.", plural(t.total, "row"), t.refName()))
	ok := importConfirmedMsg{tabID: t.id}
	if cfg.Env != config.EnvProd {
		return overlay.NewPreview(title, lines, ok)
	}
	lines = append(lines, "", "Type "+t.ref.Name+" to confirm.")
	return overlay.NewConfirm("PROD · "+title, lines, t.ref.Name, ok, overlay.WithDanger(insert))
}

// start runs the confirmed import on its own session.
func (t *importTab) start() tea.Cmd {
	if t.job == nil {
		return nil
	}
	job := *t.job
	t.job = nil
	t.gen++
	t.phase, t.progress = importRunning, new(atomic.Int64)
	ctx, cancel := context.WithTimeout(context.Background(), importTimeout)
	t.cancel = cancel
	id, b, conn, ref, create, progress := t.id, t.backend, t.conn, t.ref, t.create, t.progress
	run := func() tea.Msg {
		defer cancel()
		begin := time.Now()
		msg := importDoneMsg{tabID: id, conn: conn, ref: ref, create: create}
		c, err := b.Conn(ctx, conn, ref.Database)
		if err == nil {
			var s db.Session
			if s, err = c.Session(ctx); err == nil {
				msg.rows, err = importer.Run(ctx, s, job, progress)
				_ = s.Close()
			}
		}
		if db.IsConnLost(err) {
			b.Reset(conn)
		}
		msg.err, msg.took = err, time.Since(begin)
		slog.Info("import", "conn", conn, "table", ref.Name, "rows", msg.rows, "took", msg.took, "failed", err != nil)
		return msg
	}
	return tea.Batch(run, t.tickCmd())
}

func (t *importTab) done(m importDoneMsg) {
	t.phase, t.cancel = importIdle, nil
	if m.err != nil {
		t.setStatus(importErrorText(m.err), true)
		return
	}
	t.setStatus(fmt.Sprintf("imported %s into %s in %s", plural(m.rows, "row"), t.refName(), m.took.Round(time.Millisecond)), false)
}

// importErrorText says what a failed import did: never anything.
func importErrorText(err error) string {
	suffix := ""
	var left *importer.LeftoverError
	if errors.As(err, &left) {
		suffix, err = " · table "+left.Table+" was created but is empty; drop it manually", left.Err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "import cancelled, nothing was written" + suffix
	case errors.Is(err, context.DeadlineExceeded):
		return "import timed out, nothing was written" + suffix
	case db.IsConnLost(err):
		return "connection lost: the transaction is gone and nothing was committed" + suffix
	}
	return "import failed, nothing was written: " + err.Error() + suffix
}

func (t *importTab) progressText() string {
	if t.progress == nil {
		return ""
	}
	n := int(t.progress.Load())
	if t.phase == importChecking {
		return fmt.Sprintf("checking… %s read", plural(n, "row"))
	}
	return fmt.Sprintf("inserting %d / %s", n, plural(t.total, "row"))
}

func (t *importTab) cancelRun() tea.Cmd {
	if t.phase == importIdle || t.cancel == nil {
		t.setStatus("nothing is running", false)
		return nil
	}
	t.cancel()
	return nil
}

// shutdown cancels a running check or import when the tab closes; the
// import then rolls back.
func (t *importTab) shutdown() func() {
	c := t.cancel
	return func() {
		if c != nil {
			c()
		}
	}
}
