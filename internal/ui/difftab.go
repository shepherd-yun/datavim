package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/schemadiff"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// compareTimeout bounds reading both sides of a comparison; a namespace
// takes a few queries per table.
const compareTimeout = 5 * time.Minute

// compareSide is one side of a comparison: a table, or a whole namespace
// when ref.Name is empty.
type compareSide struct {
	conn   string
	driver string
	ref    db.TableRef
}

func (s compareSide) isTable() bool { return s.ref.Name != "" }

// label names the side for people: "local · app.users".
func (s compareSide) label() string {
	var parts []string
	for _, p := range []string{s.ref.Database, s.ref.Schema, s.ref.Name} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return s.conn + " · " + strings.Join(parts, ".")
}

// name is the side's last name: the table, the schema or the database.
func (s compareSide) name() string {
	for _, p := range []string{s.ref.Name, s.ref.Schema, s.ref.Database} {
		if p != "" {
			return p
		}
	}
	return s.conn
}

func diffTabKey(src, tgt compareSide) string {
	return "diff\x00" + tableKey(src.conn, src.ref) + "\x00" + tableKey(tgt.conn, tgt.ref)
}

type diffLoadedMsg struct {
	tabID            int
	gen              int
	srcConn, tgtConn string
	src, tgt         []db.TableSchema
	writer           db.DDLWriter // the target's
	srcErr, tgtErr   error
}

func (m diffLoadedMsg) TabID() int { return m.tabID }

func (m diffLoadedMsg) failure() (string, error) {
	if m.srcErr != nil {
		return m.srcConn, m.srcErr
	}
	return m.tgtConn, m.tgtErr
}

// readSide reads the schema of the side's table, or of every table (not
// view) of its namespace, and the DDL writer of its connection.
func readSide(ctx context.Context, b Backend, s compareSide) ([]db.TableSchema, db.DDLWriter, error) {
	var out []db.TableSchema
	var w db.DDLWriter
	err := withConn(ctx, b, s.conn, s.ref.Database, func(c db.Conn) error {
		out, w = nil, c.Dialect().DDL()
		m := c.Meta()
		if s.isTable() {
			ts, err := m.TableSchema(ctx, s.ref)
			out = append(out, ts)
			return err
		}
		tables, err := m.Tables(ctx, s.ref.Namespace)
		if err != nil {
			return err
		}
		for _, t := range tables {
			if t.Kind != db.TableKindTable {
				continue
			}
			ts, err := m.TableSchema(ctx, db.TableRef{Namespace: s.ref.Namespace, Name: t.Name})
			if err != nil {
				return err
			}
			out = append(out, ts)
		}
		return nil
	})
	return out, w, err
}

// pick addresses a checkbox: change -1 is the object's own row.
type pick struct{ obj, change int }

// diffRow is one line of the list; change -1 is the object's own row.
type diffRow struct{ obj, change int }

// diffTab compares the structure of two tables or two namespaces and
// writes the script that makes the target like the source.
type diffTab struct {
	id      int
	key     string
	keys    *keymap.Keymap
	backend Backend
	src     compareSide
	tgt     compareSide
	now     func() time.Time
	maxRows int       // compare_max_rows: rows the data view reads per side
	data    *dataView // the data view, while it is shown

	loading   bool
	cancel    context.CancelFunc
	gen       int
	srcErr    string
	tgtErr    string
	srcTables []db.TableSchema
	tgtTables []db.TableSchema
	writer    db.DDLWriter

	objects   []schemadiff.ObjectDiff
	identical int
	writable  map[pick]bool
	picked    map[pick]bool
	collapsed map[int]bool

	cursor, offset int
	width, height  int
	status         string
	statusErr      bool
}

func newDiffTab(id int, b Backend, src, tgt compareSide) *diffTab {
	return &diffTab{
		id: id, key: diffTabKey(src, tgt), backend: b, src: src, tgt: tgt,
		now: time.Now, keys: keymap.Default(), maxRows: config.DefaultSettings().CompareMaxRows,
	}
}

func (t *diffTab) ID() int                       { return t.id }
func (t *diffTab) Title() string                 { return "diff:" + t.tgt.name() }
func (t *diffTab) Key() string                   { return t.key }
func (t *diffTab) Context() keymap.Context       { return keymap.Diff }
func (t *diffTab) Target() (string, db.TableRef) { return t.tgt.conn, t.tgt.ref }
func (t *diffTab) Init() tea.Cmd                 { return t.load() }
func (t *diffTab) SetSize(w, h int)              { t.width, t.height = max(w, 0), max(h, 0) }

func (t *diffTab) usesConn(conn string) bool { return conn == t.src.conn || conn == t.tgt.conn }

func (t *diffTab) retryAfterPassword() tea.Cmd {
	if t.data != nil {
		return t.loadData()
	}
	return t.load()
}

func (t *diffTab) setStatus(s string, isErr bool) { t.status, t.statusErr = s, isErr }

// load reads both sides again; a load still running is cancelled.
func (t *diffTab) load() tea.Cmd {
	if t.cancel != nil {
		t.cancel()
	}
	t.gen++
	t.loading, t.srcErr, t.tgtErr = true, "", ""
	ctx, cancel := context.WithTimeout(context.Background(), compareTimeout)
	t.cancel = cancel
	id, gen, b, src, tgt := t.id, t.gen, t.backend, t.src, t.tgt
	return func() tea.Msg {
		defer cancel()
		msg := diffLoadedMsg{tabID: id, gen: gen, srcConn: src.conn, tgtConn: tgt.conn}
		msg.src, _, msg.srcErr = readSide(ctx, b, src)
		msg.tgt, msg.writer, msg.tgtErr = readSide(ctx, b, tgt)
		return msg
	}
}

func (t *diffTab) cancelRun() tea.Cmd {
	switch {
	case t.data != nil && t.data.loading && t.data.cancel != nil:
		t.data.cancel()
	case t.data == nil && t.loading && t.cancel != nil:
		t.cancel()
	default:
		t.setStatus("nothing is loading", false)
	}
	return nil
}

func sideError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return err.Error()
}

func (t *diffTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case diffLoadedMsg:
		t.structureLoaded(m)
	case dataLoadedMsg:
		t.dataLoaded(m)
	case dataFilterMsg:
		return t.filterData(m.text)
	}
	return nil
}

func (t *diffTab) structureLoaded(m diffLoadedMsg) {
	if m.gen != t.gen {
		return
	}
	t.loading, t.cancel = false, nil
	t.srcErr, t.tgtErr = sideError(m.srcErr), sideError(m.tgtErr)
	if t.srcErr != "" || t.tgtErr != "" {
		t.objects = nil
		return
	}
	t.srcTables, t.tgtTables, t.writer = m.src, m.tgt, m.writer
	t.compute()
}

// compute diffs the loaded sides and resets the choices to their defaults:
// what can be scripted and deletes nothing is picked.
func (t *diffTab) compute() {
	t.objects, t.identical = nil, 0
	if t.src.isTable() {
		src, tgt := &t.srcTables[0], &t.tgtTables[0]
		if ch := schemadiff.DiffTable(*tgt, *src, nil); len(ch) > 0 {
			t.objects = []schemadiff.ObjectDiff{{Kind: schemadiff.AlterTable, Name: tgt.Ref.Name, Source: src, Target: tgt, Changes: ch}}
		} else {
			t.identical = 1
		}
	} else {
		t.objects, t.identical = schemadiff.DiffNamespace(t.tgtTables, t.srcTables)
	}
	t.writable, t.picked, t.collapsed = map[pick]bool{}, map[pick]bool{}, map[int]bool{}
	for oi, o := range t.objects {
		if o.Kind != schemadiff.AlterTable {
			t.writable[pick{oi, -1}] = true
			t.picked[pick{oi, -1}] = o.Kind == schemadiff.CreateTable
			continue
		}
		for ci, c := range o.Changes {
			w := schemadiff.Writable(t.writer, o.Target.Ref, c)
			t.writable[pick{oi, ci}] = w
			t.picked[pick{oi, ci}] = w && c.Risk&db.Destructive == 0
		}
	}
	t.cursor, t.offset = 0, 0
}

func (t *diffTab) rows() []diffRow {
	var out []diffRow
	for oi, o := range t.objects {
		out = append(out, diffRow{oi, -1})
		if o.Kind == schemadiff.AlterTable && !t.collapsed[oi] {
			for ci := range o.Changes {
				out = append(out, diffRow{oi, ci})
			}
		}
	}
	return out
}

func (t *diffTab) current() (diffRow, bool) {
	rows := t.rows()
	if len(rows) == 0 {
		return diffRow{}, false
	}
	return rows[textutil.Clamp(t.cursor, 0, len(rows)-1)], true
}

func (t *diffTab) listHeight() int { return max(t.height-2, 1) }

func (t *diffTab) move(delta int) {
	t.cursor = textutil.Clamp(t.cursor+delta, 0, max(len(t.rows())-1, 0))
}

func (t *diffTab) Handle(a keymap.Action) tea.Cmd {
	t.setStatus("", false)
	if t.data != nil {
		return t.handleData(a)
	}
	switch a {
	case keymap.Down:
		t.move(1)
	case keymap.Up:
		t.move(-1)
	case keymap.Top:
		t.cursor = 0
	case keymap.Bottom:
		t.move(len(t.rows()))
	case keymap.HalfPageDown:
		t.move(max(t.listHeight()/2, 1))
	case keymap.HalfPageUp:
		t.move(-max(t.listHeight()/2, 1))
	case keymap.Expand:
		t.setCollapsed(false)
	case keymap.Collapse:
		t.setCollapsed(true)
	case keymap.NextChange:
		t.jump(1)
	case keymap.PrevChange:
		t.jump(-1)
	case keymap.ToggleChange:
		t.toggle()
	case keymap.SwapSides:
		t.swap()
	case keymap.Refresh:
		return t.load()
	case keymap.WriteScript:
		return t.writeScript()
	case keymap.DataDiff:
		return t.openData()
	case keymap.FilterPrompt:
		t.setStatus(joinHints("the filter narrows a data compare", keyHint(t.keys, keymap.Diff, keymap.DataDiff, "compares the data")), false)
	}
	return nil
}

// setCollapsed folds or unfolds the table under the cursor; folding from a
// change moves the cursor up to its table.
func (t *diffTab) setCollapsed(collapse bool) {
	r, ok := t.current()
	if !ok || t.objects[r.obj].Kind != schemadiff.AlterTable {
		return
	}
	t.collapsed[r.obj] = collapse
	if collapse {
		t.cursor = slices.Index(t.rows(), diffRow{r.obj, -1})
	}
}

// jump moves to the next (dir 1) or previous (dir -1) row that is a change:
// a change, a table to create or drop, or a folded table.
func (t *diffTab) jump(dir int) {
	rows := t.rows()
	for i := t.cursor + dir; i >= 0 && i < len(rows); i += dir {
		r := rows[i]
		if r.change >= 0 || t.objects[r.obj].Kind != schemadiff.AlterTable || t.collapsed[r.obj] {
			t.cursor = i
			return
		}
	}
	t.setStatus("no more changes", false)
}

// toggle flips the checkbox under the cursor. On an altered table's row it
// picks all of its changes, or none when all are picked already.
func (t *diffTab) toggle() {
	r, ok := t.current()
	if !ok {
		return
	}
	o := t.objects[r.obj]
	if r.change >= 0 || o.Kind != schemadiff.AlterTable {
		p := pick(r)
		if !t.writable[p] {
			if o.Changes[r.change].Kind == db.ForeignKeyDiff {
				t.setStatus("foreign keys are compared, not synced", true)
			} else {
				t.setStatus("this change cannot be scripted · make it by hand", true)
			}
			return
		}
		t.picked[p] = !t.picked[p]
		return
	}
	all := true
	for ci := range o.Changes {
		if p := (pick{r.obj, ci}); t.writable[p] && !t.picked[p] {
			all = false
		}
	}
	for ci := range o.Changes {
		if p := (pick{r.obj, ci}); t.writable[p] {
			t.picked[p] = !all
		}
	}
}

// swap makes the target the source and computes the diff again.
func (t *diffTab) swap() {
	if t.loading || t.srcErr != "" || t.tgtErr != "" || t.writer == nil {
		t.setStatus("both sides have to be loaded first", true)
		return
	}
	t.src, t.tgt = t.tgt, t.src
	t.srcTables, t.tgtTables = t.tgtTables, t.srcTables
	t.compute()
	t.setStatus("now "+t.src.label()+" ──▶ "+t.tgt.label(), false)
}

// skippedDestructive counts the deletions left out of the script.
func (t *diffTab) skippedDestructive() int {
	n := 0
	for oi, o := range t.objects {
		if o.Kind == schemadiff.DropTable && !t.picked[pick{oi, -1}] {
			n++
		}
		for ci, c := range o.Changes {
			if p := (pick{oi, ci}); c.Risk&db.Destructive != 0 && t.writable[p] && !t.picked[p] {
				n++
			}
		}
	}
	return n
}

// box is a row's checkbox: [✓], [ ], [-] for a partly picked table, or
// blank when nothing on the row can be scripted.
func (t *diffTab) box(r diffRow) string {
	o := t.objects[r.obj]
	if r.change >= 0 || o.Kind != schemadiff.AlterTable {
		switch p := pick(r); {
		case !t.writable[p]:
			return "   "
		case t.picked[p]:
			return "[✓]"
		}
		return "[ ]"
	}
	var writable, picked int
	for ci := range o.Changes {
		if p := (pick{r.obj, ci}); t.writable[p] {
			writable++
			if t.picked[p] {
				picked++
			}
		}
	}
	switch {
	case writable == 0:
		return "   "
	case picked == writable:
		return "[✓]"
	case picked == 0:
		return "[ ]"
	}
	return "[-]"
}

// rowText is a row's text and the style its sign (+, -, ~) takes.
func (t *diffTab) rowText(r diffRow) (string, func(...string) string) {
	o := t.objects[r.obj]
	var prefix, desc, note string
	if r.change < 0 {
		fold := " "
		if o.Kind == schemadiff.AlterTable {
			fold = "▾"
			if t.collapsed[r.obj] {
				fold = "▸"
			}
		}
		prefix, desc = " "+fold+" "+t.box(r)+" ", schemadiff.DescribeObject(o)
		if o.Kind == schemadiff.DropTable {
			note = "  ⚠ destructive"
		}
	} else {
		c := o.Changes[r.change]
		prefix, desc = "     "+t.box(r)+" ", schemadiff.Describe(c)
		switch {
		case c.Kind == db.ForeignKeyDiff:
			note = "  (not synced)"
		case !t.writable[pick(r)]:
			note = "  ⚠ manual"
		case c.Risk&db.Destructive != 0:
			note = "  ⚠ destructive"
		case c.Risk&db.Lossy != 0:
			note = "  ⚠ lossy"
		}
	}
	style := theme.Changed.Render
	switch {
	case strings.HasPrefix(desc, "+"):
		style = theme.Inserted.Render
	case strings.HasPrefix(desc, "-"):
		style = theme.Error.Render
	}
	return textutil.Sanitize(prefix + desc + note), style
}

func (t *diffTab) header() string {
	head := theme.Title.Render(textutil.Sanitize(t.src.label() + " ──▶ " + t.tgt.label()))
	if cfg, _ := t.backend.Connection(t.tgt.conn); cfg.Env == config.EnvProd {
		head += " " + theme.Prod.Render("[PROD]")
	}
	if t.loading || t.srcErr != "" || t.tgtErr != "" || len(t.objects) == 0 {
		return head
	}
	parts := []string{fmt.Sprintf("%d tables differ", len(t.objects))}
	if len(t.objects) == 1 {
		parts[0] = "1 table differs"
	}
	if t.identical > 0 {
		parts = append(parts, fmt.Sprintf("%d identical", t.identical))
	}
	if n := t.skippedDestructive(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d destructive skipped", n))
	}
	return head + "   " + theme.Muted.Render(strings.Join(parts, " · "))
}

func (t *diffTab) body() []string {
	var out []string
	switch {
	case t.srcErr != "" || t.tgtErr != "":
		if t.srcErr != "" {
			out = append(out, theme.Error.Render(textutil.Sanitize("✗ source "+t.src.label()+": "+t.srcErr)))
		}
		if t.tgtErr != "" {
			out = append(out, theme.Error.Render(textutil.Sanitize("✗ target "+t.tgt.label()+": "+t.tgtErr)))
		}
		if h := keyHint(t.keys, keymap.Global, keymap.Refresh, "reloads"); h != "" {
			out = append(out, theme.Muted.Render(h))
		}
	case t.loading:
		out = append(out, theme.Muted.Render(joinHints("loading both sides…", keyHint(t.keys, keymap.Global, keymap.Cancel, "cancels"))))
	case len(t.objects) == 0 && t.src.isTable():
		out = append(out, theme.Muted.Render("the tables have the same structure"))
	case len(t.objects) == 0:
		out = append(out, theme.Muted.Render(fmt.Sprintf("no differences · %d tables alike", t.identical)))
	default:
		out = t.listLines()
	}
	return out
}

func (t *diffTab) listLines() []string {
	rows := t.rows()
	h := t.listHeight()
	t.cursor = textutil.Clamp(t.cursor, 0, len(rows)-1)
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+h {
		t.offset = t.cursor - h + 1
	}
	t.offset = textutil.Clamp(t.offset, 0, max(len(rows)-h, 0))
	var out []string
	for i := t.offset; i < min(t.offset+h, len(rows)); i++ {
		text, style := t.rowText(rows[i])
		text = textutil.Fit(text, t.width)
		if i == t.cursor {
			out = append(out, theme.Selected.Render(text))
		} else {
			out = append(out, style(text))
		}
	}
	return out
}

func (t *diffTab) statusLine() string {
	if t.status != "" {
		if t.statusErr {
			return theme.Error.Render(t.status)
		}
		return t.status
	}
	if t.data != nil {
		return t.dataHints()
	}
	return theme.Muted.Render(joinHints(
		keyHint(t.keys, keymap.Diff, keymap.ToggleChange, "include/skip"),
		keyHint(t.keys, keymap.Diff, keymap.NextChange, "next change"),
		keyHint(t.keys, keymap.Diff, keymap.SwapSides, "swap"),
		keyHint(t.keys, keymap.Diff, keymap.WriteScript, "script to console"),
		keyHint(t.keys, keymap.Diff, keymap.DataDiff, "data"),
		keyHint(t.keys, keymap.Global, keymap.Refresh, "reload"),
	))
}

func (t *diffTab) View(bool) string {
	var lines []string
	if t.data != nil {
		lines = t.dataLines()
	} else {
		lines = append([]string{textutil.Fit(t.header(), t.width)}, t.body()...)
	}
	for len(lines) < t.height-1 {
		lines = append(lines, "")
	}
	lines = lines[:max(t.height-1, 1)]
	return strings.Join(append(lines, textutil.Fit(t.statusLine(), t.width)), "\n")
}

// writeScript asks for a console on the target holding the script of the
// picked changes.
func (t *diffTab) writeScript() tea.Cmd {
	if t.loading || len(t.objects) == 0 {
		t.setStatus("nothing to sync", true)
		return nil
	}
	stmts, manual := schemadiff.Script(t.writer, t.tgt.ref.Namespace, t.objects, func(obj, change int) bool {
		return t.picked[pick{obj, change}]
	})
	if len(stmts) == 0 {
		msg := "nothing picked"
		if len(manual) > 0 {
			msg += " · " + plural(len(manual), "change") + " to make by hand"
		}
		t.setStatus(joinHints(msg, keyHint(t.keys, keymap.Diff, keymap.ToggleChange, "picks a change")), true)
		return nil
	}
	conn, ns, text := t.tgt.conn, t.tgt.ref.Namespace, t.scriptText(stmts, manual)
	return func() tea.Msg { return scriptConsoleMsg{conn: conn, ns: ns, text: text} }
}

func (t *diffTab) scriptText(stmts, manual []string) string {
	var b strings.Builder
	b.WriteString("-- sqlvim schema sync\n")
	fmt.Fprintf(&b, "-- source: %s\n-- target: %s\n-- generated: %s\n",
		t.src.label(), t.tgt.label(), t.now().Format("2006-01-02 15:04:05"))
	b.WriteString("-- These statements change the target to match the source. Review them before running.\n")
	for _, s := range stmts {
		b.WriteString("\n" + s + ";\n")
	}
	if len(manual) > 0 {
		b.WriteString("\n-- Not scripted, change by hand:\n")
		for _, l := range manual {
			b.WriteString("--   " + strings.ReplaceAll(l, "\n", " ") + "\n")
		}
	}
	return b.String()
}
