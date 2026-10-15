package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/datadiff"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/schemadiff"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/complete"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// dataCellWidth caps a column of the data view; l / h scroll the columns
// that do not fit.
const dataCellWidth = 30

// dataPrefix is the width of " [✓] ~ " before a row's cells.
const dataPrefix = 7

// dataView compares the rows of one pair of tables (spec §5.4). The diff
// tab shows it instead of the structure while it is set.
type dataView struct {
	src, tgt compareSide
	spec     datadiff.Spec
	columns  []db.Column // the compared columns, for F's completion
	where    string      // the condition both sides are read with; "" for every row

	loading        bool
	cancel         context.CancelFunc
	srcErr, tgtErr string
	srcCols        []db.ColumnInfo // as the source's query returned them
	tgtCols        []db.ColumnInfo
	rows           []datadiff.Row
	compared       int // the target's rows read
	picked         []bool
	widths         []int // per compared column

	cursor, offset, colOffset int
}

type dataLoadedMsg struct {
	tabID            int
	gen              int
	srcConn, tgtConn string
	rows             []datadiff.Row
	compared         int
	srcCols, tgtCols []db.ColumnInfo
	srcErr, tgtErr   error
}

func (m dataLoadedMsg) TabID() int { return m.tabID }

func (m dataLoadedMsg) failure() (string, error) {
	if m.srcErr != nil {
		return m.srcConn, m.srcErr
	}
	return m.tgtConn, m.tgtErr
}

// readData reads the source's rows, then pairs the target's with them as
// they stream in. Each attempt at a side starts it over, so a retry after
// a reconnect counts no row twice.
func readData(ctx context.Context, b Backend, src, tgt compareSide, spec datadiff.Spec, where string, limit int) dataLoadedMsg {
	msg := dataLoadedMsg{srcConn: src.conn, tgtConn: tgt.conn}
	d := sqlx.DialectOf(src.driver)
	var rows *datadiff.Source
	msg.srcErr = withConn(ctx, b, src.conn, src.ref.Database, func(c db.Conn) error {
		rows = spec.NewSource(limit)
		return c.Stream(ctx, spec.Query(d, src.ref, where), func(cols []db.ColumnInfo) { msg.srcCols = cols }, rows.Add)
	})
	if msg.srcErr != nil {
		return msg
	}
	var match *datadiff.Matcher
	msg.tgtErr = withConn(ctx, b, tgt.conn, tgt.ref.Database, func(c db.Conn) error {
		match = rows.Match()
		return c.Stream(ctx, spec.Query(d, tgt.ref, where), func(cols []db.ColumnInfo) { msg.tgtCols = cols }, match.Add)
	})
	if msg.tgtErr == nil {
		msg.rows, msg.compared = match.Rows(), match.Len()
	}
	return msg
}

// openData shows the data view of the tables compared: the two tables, or
// the ~ table under the cursor in a namespace comparison.
func (t *diffTab) openData() tea.Cmd {
	if t.loading || t.srcErr != "" || t.tgtErr != "" || t.writer == nil {
		t.setStatus("both sides have to be loaded first", true)
		return nil
	}
	var src, tgt *db.TableSchema
	if t.src.isTable() {
		src, tgt = &t.srcTables[0], &t.tgtTables[0]
	} else if r, ok := t.current(); ok && t.objects[r.obj].Kind == schemadiff.AlterTable {
		src, tgt = t.objects[r.obj].Source, t.objects[r.obj].Target
	}
	if src == nil {
		t.setStatus("only tables both sides have compare their data · move to a ~ table", true)
		return nil
	}
	spec, err := datadiff.NewSpec(*src, *tgt)
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	var cols []db.Column
	for _, c := range src.Columns {
		if slices.Contains(spec.Columns, c.Name) {
			cols = append(cols, c)
		}
	}
	t.data = &dataView{
		src:  compareSide{conn: t.src.conn, driver: t.src.driver, ref: src.Ref},
		tgt:  compareSide{conn: t.tgt.conn, driver: t.tgt.driver, ref: tgt.Ref},
		spec: spec, columns: cols,
	}
	return t.loadData()
}

// closeData goes back to the structure, abandoning a load.
func (t *diffTab) closeData() {
	if t.data.cancel != nil {
		t.data.cancel()
	}
	t.data = nil
}

// loadData reads both sides' rows again; a load still running is cancelled.
func (t *diffTab) loadData() tea.Cmd {
	v := t.data
	if v.cancel != nil {
		v.cancel()
	}
	t.gen++
	v.loading, v.srcErr, v.tgtErr = true, "", ""
	ctx, cancel := context.WithTimeout(context.Background(), compareTimeout)
	v.cancel = cancel
	id, gen, b, limit := t.id, t.gen, t.backend, t.maxRows
	src, tgt, spec, where := v.src, v.tgt, v.spec, v.where
	return func() tea.Msg {
		defer cancel()
		msg := readData(ctx, b, src, tgt, spec, where, limit)
		msg.tabID, msg.gen = id, gen
		return msg
	}
}

func (t *diffTab) dataLoaded(m dataLoadedMsg) {
	v := t.data
	if v == nil || m.gen != t.gen {
		return
	}
	v.loading, v.cancel = false, nil
	v.srcErr, v.tgtErr = t.dataError(m.srcErr), t.dataError(m.tgtErr)
	if v.srcErr != "" || v.tgtErr != "" {
		v.rows = nil
		return
	}
	v.srcCols, v.tgtCols, v.rows, v.compared = m.srcCols, m.tgtCols, m.rows, m.compared
	v.reset()
}

// dataError is sideError, pointing a side over the row limit at F.
func (t *diffTab) dataError(err error) string {
	s := sideError(err)
	var tm datadiff.TooManyRowsError
	if errors.As(err, &tm) {
		s = joinHints(s, keyHint(t.keys, keymap.Diff, keymap.FilterPrompt, "narrows the rows compared"))
	}
	return s
}

// reset picks every row but the deletions, puts the cursor on the first
// row and measures the columns.
func (v *dataView) reset() {
	v.picked = make([]bool, len(v.rows))
	for i, r := range v.rows {
		v.picked[i] = r.Status != datadiff.Removed
	}
	v.cursor, v.offset, v.colOffset = 0, 0, 0
	v.widths = make([]int, len(v.spec.Columns))
	for c, name := range v.spec.Columns {
		v.widths[c] = ansi.StringWidth(textutil.Sanitize(name))
	}
	for _, r := range v.rows {
		for c := range v.widths {
			text, _ := cellText(r, c)
			v.widths[c] = max(v.widths[c], ansi.StringWidth(text))
		}
	}
	for c := range v.widths {
		v.widths[c] = min(v.widths[c], dataCellWidth)
	}
}

// cellText is column c of r as shown: the source's value of a new row, the
// target's of a deleted one, "target → source" where they differ.
func cellText(r datadiff.Row, c int) (text string, differs bool) {
	switch {
	case r.Status == datadiff.Removed:
		return valueText(r.Target[c]), false
	case r.Differs(c):
		return valueText(r.Target[c]) + " → " + valueText(r.Source[c]), true
	}
	return valueText(r.Source[c]), false
}

func valueText(v db.Value) string {
	if v.Null {
		return "NULL"
	}
	return textutil.Sanitize(v.Text)
}

// others are the columns that are not the key, in compared order.
func (v *dataView) others() []int {
	key := v.spec.KeyIndexes()
	var out []int
	for c := range v.spec.Columns {
		if !slices.Contains(key, c) {
			out = append(out, c)
		}
	}
	return out
}

// shown are the columns drawn: the key, then the others from colOffset.
func (v *dataView) shown() []int {
	others := v.others()
	return append(v.spec.KeyIndexes(), others[min(v.colOffset, len(others)):]...)
}

func (v *dataView) scrollColumns(delta int) {
	v.colOffset = textutil.Clamp(v.colOffset+delta, 0, max(len(v.others())-1, 0))
}

func (v *dataView) move(delta int) {
	v.cursor = textutil.Clamp(v.cursor+delta, 0, max(len(v.rows)-1, 0))
}

func (t *diffTab) dataListHeight() int { return max(t.height-5, 1) }

// dataJump moves to the next (dir 1) or previous (dir -1) row: every row
// of the data view is a change.
func (t *diffTab) dataJump(dir int) {
	v := t.data
	if i := v.cursor + dir; i >= 0 && i < len(v.rows) {
		v.cursor = i
		return
	}
	t.setStatus("no more changes", false)
}

// handleData runs an action in the data view.
func (t *diffTab) handleData(a keymap.Action) tea.Cmd {
	v := t.data
	switch a {
	case keymap.Down:
		v.move(1)
	case keymap.Up:
		v.move(-1)
	case keymap.Top:
		v.cursor = 0
	case keymap.Bottom:
		v.move(len(v.rows))
	case keymap.HalfPageDown:
		v.move(max(t.dataListHeight()/2, 1))
	case keymap.HalfPageUp:
		v.move(-max(t.dataListHeight()/2, 1))
	case keymap.NextChange:
		t.dataJump(1)
	case keymap.PrevChange:
		t.dataJump(-1)
	case keymap.Expand:
		v.scrollColumns(1)
	case keymap.Collapse:
		v.scrollColumns(-1)
	case keymap.ToggleChange:
		t.toggleData()
	case keymap.SwapSides:
		t.swapData()
	case keymap.FilterPrompt:
		return t.dataFilterPrompt()
	case keymap.WriteScript:
		return t.writeDataScript()
	case keymap.DataDiff:
		t.closeData()
	case keymap.Refresh:
		return t.loadData()
	}
	return nil
}

func (v *dataView) skippedDeletes() int {
	n := 0
	for i, r := range v.rows {
		if r.Status == datadiff.Removed && !v.picked[i] {
			n++
		}
	}
	return n
}

func (t *diffTab) dataHeader() string {
	v := t.data
	head := theme.Title.Render(textutil.Sanitize(v.src.label() + " ──▶ " + v.tgt.label()))
	if cfg, _ := t.backend.Connection(v.tgt.conn); cfg.Env == config.EnvProd {
		head += " " + theme.Prod.Render("[PROD]")
	}
	parts := []string{"key: " + strings.Join(v.spec.Key, ", ")}
	if !v.loading && v.srcErr == "" && v.tgtErr == "" && len(v.rows) > 0 {
		added, removed, changed := datadiff.Count(v.rows)
		verb := "differ"
		if len(v.rows) == 1 {
			verb = "differs"
		}
		parts = append(parts, fmt.Sprintf("%d %s (%d + · %d - · %d ~)", len(v.rows), verb, added, removed, changed))
		if n := v.skippedDeletes(); n > 0 {
			parts = append(parts, plural(n, "delete")+" skipped")
		}
	}
	return head + "   " + theme.Muted.Render(textutil.Sanitize(strings.Join(parts, "   ")))
}

// note says what narrows the comparison: the filter, and columns only one
// side has.
func (v *dataView) note() string {
	var parts []string
	if v.where != "" {
		parts = append(parts, "WHERE "+textutil.Sanitize(v.where))
	}
	if len(v.spec.SourceOnly)+len(v.spec.TargetOnly) > 0 {
		parts = append(parts, "the structures differ · comparing the "+plural(len(v.spec.Columns), "column")+" both have")
	}
	return strings.Join(parts, "   ")
}

// dataLines are the data view's lines above the status line.
func (t *diffTab) dataLines() []string {
	v := t.data
	out := []string{textutil.Fit(t.dataHeader(), t.width)}
	if n := v.note(); n != "" {
		out = append(out, textutil.Fit(theme.Muted.Render(n), t.width))
	}
	switch {
	case v.srcErr != "" || v.tgtErr != "":
		if v.srcErr != "" {
			out = append(out, theme.Error.Render(textutil.Sanitize("✗ source "+v.src.label()+": "+v.srcErr)))
		}
		if v.tgtErr != "" {
			out = append(out, theme.Error.Render(textutil.Sanitize("✗ target "+v.tgt.label()+": "+v.tgtErr)))
		}
		if h := keyHint(t.keys, keymap.Global, keymap.Refresh, "reloads"); h != "" {
			out = append(out, theme.Muted.Render(h))
		}
	case v.loading:
		out = append(out, theme.Muted.Render(joinHints("reading both sides…", keyHint(t.keys, keymap.Global, keymap.Cancel, "cancels"))))
	case len(v.rows) == 0:
		out = append(out, theme.Muted.Render("the rows are the same · "+plural(v.compared, "row")+" compared"))
	default:
		out = append(out, t.dataTable(t.height-1-len(out))...)
	}
	return out
}

// dataTable is the column header and the rows that fit in h lines.
func (t *diffTab) dataTable(h int) []string {
	v := t.data
	cols := v.shown()
	head := make([]string, len(cols))
	for j, c := range cols {
		head[j] = textutil.Fit(textutil.Sanitize(v.spec.Columns[c]), v.widths[c])
	}
	out := []string{textutil.Fit(theme.Header.Render(strings.Repeat(" ", dataPrefix)+strings.Join(head, "  ")), t.width)}
	h = max(h-1, 1)
	v.cursor = textutil.Clamp(v.cursor, 0, len(v.rows)-1)
	if v.cursor < v.offset {
		v.offset = v.cursor
	}
	if v.cursor >= v.offset+h {
		v.offset = v.cursor - h + 1
	}
	v.offset = textutil.Clamp(v.offset, 0, max(len(v.rows)-h, 0))
	for i := v.offset; i < min(v.offset+h, len(v.rows)); i++ {
		plain, styled := v.line(i, cols)
		if i == v.cursor {
			out = append(out, theme.Selected.Render(textutil.Fit(plain, t.width)))
		} else {
			out = append(out, textutil.Fit(styled, t.width))
		}
	}
	return out
}

// line is row i, plain and styled: its checkbox, its sign and the cells of
// cols, the differing ones highlighted.
func (v *dataView) line(i int, cols []int) (plain, styled string) {
	r := v.rows[i]
	box := "[ ]"
	if v.picked[i] {
		box = "[✓]"
	}
	sign, style := "~", theme.Changed.Render
	switch r.Status {
	case datadiff.Added:
		sign, style = "+", theme.Inserted.Render
	case datadiff.Removed:
		sign, style = "-", theme.Error.Render
	}
	prefix := " " + box + " " + sign + " "
	ps, ss := make([]string, len(cols)), make([]string, len(cols))
	for j, c := range cols {
		text, differs := cellText(r, c)
		ps[j] = textutil.Fit(text, v.widths[c])
		ss[j] = ps[j]
		if differs {
			ss[j] = theme.Changed.Render(ps[j])
		}
	}
	return prefix + strings.Join(ps, "  "), style(prefix) + strings.Join(ss, "  ")
}

func (t *diffTab) dataHints() string {
	return theme.Muted.Render(joinHints(
		keyHint(t.keys, keymap.Diff, keymap.ToggleChange, "include/skip"),
		keyHint(t.keys, keymap.Diff, keymap.FilterPrompt, "filter"),
		keyHint(t.keys, keymap.Diff, keymap.SwapSides, "swap"),
		keyHint(t.keys, keymap.Diff, keymap.WriteScript, "script to console"),
		keyHint(t.keys, keymap.Diff, keymap.DataDiff, "structure"),
	))
}

// dataFilterMsg is the condition typed at F in the data view.
type dataFilterMsg struct {
	tabID int
	text  string
}

func (m dataFilterMsg) TabID() int { return m.tabID }

// loaded reports rows to act on: the load finished without an error.
func (v *dataView) loaded() bool { return !v.loading && v.srcErr == "" && v.tgtErr == "" }

func (t *diffTab) toggleData() {
	v := t.data
	if !v.loaded() || len(v.rows) == 0 {
		return
	}
	v.picked[v.cursor] = !v.picked[v.cursor]
}

// swapData makes the target the source; the rows read are reused.
func (t *diffTab) swapData() {
	v := t.data
	if !v.loaded() {
		t.setStatus("both sides have to be loaded first", true)
		return
	}
	v.src, v.tgt = v.tgt, v.src
	v.srcCols, v.tgtCols = v.tgtCols, v.srcCols
	v.spec = v.spec.Swap()
	v.rows = datadiff.SwapRows(v.rows)
	v.reset()
	t.setStatus("now "+v.src.label()+" ──▶ "+v.tgt.label(), false)
}

// dataFilterPrompt edits the condition both sides are read with, starting
// from the current one, else the first key column.
func (t *diffTab) dataFilterPrompt() tea.Cmd {
	v := t.data
	d := sqlx.DialectOf(v.src.driver)
	initial := v.where
	if initial == "" && len(v.spec.Key) > 0 {
		initial = sqlx.QuoteIfNeeded(v.spec.Key[0], d) + " "
	}
	id, cols, spec, ref := t.id, v.columns, v.spec, v.src.ref
	return openOverlay(overlay.NewPrompt("Filter both sides",
		"WHERE …  read on the source and the target · empty compares every row", initial,
		func(s string) tea.Msg { return dataFilterMsg{tabID: id, text: s} },
		overlay.WithCompleter(func(text string, cursor int) (int, []complete.Candidate) {
			return completeCondition(d, cols, text, cursor)
		}),
		overlay.WithValidate(func(s string) error { return checkSelect(d, spec.Query(d, ref, normFilter(s))) })))
}

// filterData reads both sides again with the typed condition.
func (t *diffTab) filterData(text string) tea.Cmd {
	if t.data == nil {
		return nil
	}
	t.data.where = normFilter(text)
	return t.loadData()
}

// writeDataScript asks for a console on the target holding the INSERT,
// UPDATE and DELETE statements of the picked rows.
func (t *diffTab) writeDataScript() tea.Cmd {
	v := t.data
	if !v.loaded() || len(v.rows) == 0 {
		t.setStatus("nothing to sync", true)
		return nil
	}
	stmts, err := v.spec.Script(sqlx.DialectOf(v.tgt.driver), v.tgt.ref, v.tgtCols, v.rows, func(i int) bool { return v.picked[i] })
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	if len(stmts) == 0 {
		t.setStatus(joinHints("nothing picked", keyHint(t.keys, keymap.Diff, keymap.ToggleChange, "picks a row")), true)
		return nil
	}
	conn, ns, text := v.tgt.conn, v.tgt.ref.Namespace, t.dataScriptText(stmts)
	return func() tea.Msg { return scriptConsoleMsg{conn: conn, ns: ns, text: text} }
}

func (t *diffTab) dataScriptText(stmts []string) string {
	v := t.data
	var b strings.Builder
	b.WriteString("-- sqlvim data sync\n")
	fmt.Fprintf(&b, "-- source: %s\n-- target: %s\n-- key: %s\n", v.src.label(), v.tgt.label(), strings.Join(v.spec.Key, ", "))
	if v.where != "" {
		fmt.Fprintf(&b, "-- where: %s\n", strings.ReplaceAll(v.where, "\n", " "))
	}
	fmt.Fprintf(&b, "-- generated: %s\n", t.now().Format("2006-01-02 15:04:05"))
	b.WriteString("-- These statements change the target's rows to match the source. Review them before running.\n\n")
	for _, s := range stmts {
		b.WriteString(s + ";\n")
	}
	return b.String()
}
