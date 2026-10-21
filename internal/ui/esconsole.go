package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/history"
	"github.com/shepherd-yun/datavim/internal/ui/highlight"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/panes/complete"
	"github.com/shepherd-yun/datavim/internal/ui/panes/editor"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/panes/textview"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
	"github.com/shepherd-yun/datavim/internal/ui/vimedit"
)

// esJSONLines is how many lines of a response the console renders.
const esJSONLines = 10_000

func esConsoleTabKey(conn string) string { return "esconsole\x00" + conn }

// openESConsole opens, or switches to, the REST console of conn.
func (m *Model) openESConsole(conn string) tea.Cmd {
	rec := history.Recent{Kind: history.RecentConsole, Conn: conn}
	return m.openTab(esConsoleTabKey(conn), rec, func(id int) tab {
		t := newESConsoleTab(id, m.backend, m.store, conn, m.settings, m.timings)
		t.keys = m.keys
		t.treeNames = func() []string { return m.esTreeNames(conn) }
		return t
	})
}

// esResult is one request's outcome.
type esResult struct {
	req  esx.Request
	resp es.Response
	err  error

	lines   []string   // the JSON, indented
	table   *esx.Table // the table view, made when first shown
	asTable bool
}

func (r esResult) failed() bool { return r.err != nil || r.resp.Status >= 300 }

// esConsoleRunMsg is the outcome of a run.
type esConsoleRunMsg struct {
	tabID   int
	conn    string
	gen     int
	results []esResult
	err     error // the connection could not be had
}

func (m esConsoleRunMsg) TabID() int               { return m.tabID }
func (m esConsoleRunMsg) esMsg()                   {}
func (m esConsoleRunMsg) failure() (string, error) { return m.conn, m.err }

// esResolvedMsg is the indices of a run's critical requests.
type esResolvedMsg struct {
	tabID    int
	conn     string
	gen      int
	resolved map[int][]string // by request
	err      error
}

func (m esResolvedMsg) TabID() int               { return m.tabID }
func (m esResolvedMsg) esMsg()                   {}
func (m esResolvedMsg) failure() (string, error) { return m.conn, m.err }

// esStepMsg is one confirmation of a run passed: step is the next one.
type esStepMsg struct{ tabID, gen, step int }

func (m esStepMsg) TabID() int { return m.tabID }

// esRun is a run waiting for its confirmations.
type esRun struct {
	reqs     []esx.Request
	classes  []esx.Class
	resolved map[int][]string
	steps    []esPlan
}

// esConsoleTab is an Elasticsearch connection's REST console: Kibana Dev
// Tools' file format, a result tab per request.
type esConsoleTab struct {
	id        int
	keys      *keymap.Keymap
	backend   Backend
	store     Store
	editorCmd []string
	draftGen  int
	conn      string
	timings   timings

	editor    *editor.Model
	grid      *grid.Model
	view      *textview.Model
	onResults bool
	messages  []string
	results   []esResult
	tabs      resultTabs
	clipPaste *vimedit.ClipboardPaste

	popup      *complete.Model
	popStart   int                 // byte offset the popup's word starts at
	treeNames  func() []string     // the tree's indices, aliases and data streams
	indexNames []string            // nil until read
	fieldCache map[string][]string // field names by index, until a refresh

	run     *esRun
	running bool
	cancel  context.CancelFunc
	started time.Time
	gen     int

	status    string
	statusErr bool

	width, height, editorH, resultsH int
	full, fitOnResults               bool
}

func newESConsoleTab(id int, b Backend, st Store, conn string, s config.Settings, tm timings) *esConsoleTab {
	t := &esConsoleTab{
		id: id, backend: b, store: st, conn: conn, timings: tm, editorCmd: s.EditorCommand(os.Getenv),
		editor: editor.New(), grid: grid.New(nil, s.MaxColumnWidth), view: textview.New(), keys: keymap.Default(),
	}
	t.editor.SetPlaceholder("GET _search  (a request line, then its JSON body)")
	t.editor.SetHighlighter(highlight.ES)
	return t
}

func (t *esConsoleTab) ID() int                          { return t.id }
func (t *esConsoleTab) UseRegisters(r vimedit.Registers) { t.editor.UseRegisters(r) }
func (t *esConsoleTab) Key() string                      { return esConsoleTabKey(t.conn) }
func (t *esConsoleTab) Title() string                    { return "es:" + t.conn }
func (t *esConsoleTab) Target() (string, db.TableRef)    { return t.conn, db.TableRef{} }
func (t *esConsoleTab) CapturesInput() bool              { return !t.onResults && t.editor.Insert() }
func (t *esConsoleTab) activeGrid() *grid.Model          { return t.grid }
func (t *esConsoleTab) setStatus(s string, isErr bool)   { t.status, t.statusErr = s, isErr }

func (t *esConsoleTab) Context() keymap.Context {
	if t.onResults {
		return keymap.ESResults
	}
	return keymap.Console
}

func (t *esConsoleTab) Mode() string {
	if t.onResults {
		return "NORMAL"
	}
	return t.editor.Mode().String()
}

func (t *esConsoleTab) Find(q string, forward bool) bool {
	c := t.cur()
	found := false
	switch {
	case c == nil:
	case c.asTable:
		found = t.grid.Find(q, forward)
	default:
		found = t.view.Find(q, forward)
	}
	if found {
		t.onResults = true
	}
	return found
}

func (t *esConsoleTab) exportSource() exportSrc {
	if c := t.cur(); c != nil && c.asTable {
		return exportSrc{rs: t.grid.Result(), name: "es", conn: t.conn}
	}
	return exportSrc{}
}

func (t *esConsoleTab) SetSize(w, h int) {
	t.width, t.height = max(w, 0), max(h, 0)
	t.fit()
}

func (t *esConsoleTab) SetFull(full bool) {
	if full != t.full || full && t.fitOnResults != t.onResults {
		t.full = full
		t.fit()
	}
}

func (t *esConsoleTab) fit() {
	t.editorH, t.resultsH = splitHeights(t.height, t.full, t.onResults)
	t.fitOnResults = t.onResults
	t.editor.SetSize(t.width, t.editorH)
	h := max(t.resultsH-1, 0)
	t.grid.SetSize(t.width, h)
	t.view.SetSize(t.width, h)
}

func (t *esConsoleTab) log(line string) {
	t.messages = append(t.messages, line)
	if over := len(t.messages) - maxMessages; over > 0 {
		t.messages = t.messages[over:]
	}
}

// ---- input ----

func (t *esConsoleTab) Input(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if cmd, used := t.popupKey(k.String()); used {
			return cmd
		}
		switch k.String() {
		case "ctrl+enter", "ctrl+j":
			return t.runAtCursor()
		case "ctrl+c":
			return t.cancelRun()
		}
	}
	before := t.editor.Version()
	cmd := editorEffects(t.id, t.editor.Update(msg), &t.clipPaste)
	if t.editor.Version() == before || !t.editor.Insert() {
		t.popup = nil
		return cmd
	}
	return tea.Batch(cmd, t.suggest(false), t.scheduleDraft())
}

func (t *esConsoleTab) Paste(msg tea.PasteMsg) tea.Cmd {
	if t.onResults {
		return nil
	}
	before := t.editor.Version()
	t.editor.Update(msg)
	if t.editor.Version() == before {
		return nil
	}
	return t.scheduleDraft()
}

func (t *esConsoleTab) TakeKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if t.onResults {
		return nil, false
	}
	if msg.String() == t.keys.Leader() && !t.editor.Insert() && t.editor.Pending() == "" {
		return nil, false
	}
	before := t.editor.Version()
	res := t.editor.Update(msg)
	if !res.Handled {
		return nil, false
	}
	cmd := editorEffects(t.id, res, &t.clipPaste)
	if t.editor.Version() != before {
		cmd = tea.Batch(cmd, t.scheduleDraft())
	}
	return cmd, true
}

func (t *esConsoleTab) Handle(a keymap.Action) tea.Cmd {
	switch a {
	case keymap.Cancel:
		return t.cancelRun()
	case keymap.FocusResults:
		t.onResults = true
		return nil
	case keymap.FocusEditor:
		t.onResults = false
		return nil
	case keymap.ResultView:
		t.toggleView()
		return nil
	case keymap.PrevResult, keymap.NextResult:
		d := 1
		if a == keymap.PrevResult {
			d = -1
		}
		t.selectResult(t.tabs.step(d))
		return nil
	}
	if a == keymap.Refresh {
		t.indexNames, t.fieldCache = nil, nil
		t.setStatus("completion reads the index and field names again", false)
		return nil
	}
	if t.onResults {
		return t.resultsAction(a)
	}
	switch a {
	case keymap.ExternalEdit:
		return writeTempEdit(t.id, t.editor.Value(), "datavim-*.es")
	case keymap.RunStatement:
		return t.runAtCursor()
	case keymap.RunAll:
		return t.runAll(0)
	case keymap.RunFromCursor:
		line, _ := t.editor.Cursor()
		return t.runAll(line)
	}
	return nil
}

func (t *esConsoleTab) resultsAction(a keymap.Action) tea.Cmd {
	c := t.cur()
	if c == nil {
		return nil
	}
	if a == keymap.OpenCellKey {
		return t.openCellDoc()
	}
	if c.asTable {
		cmd, _ := gridAction(t.grid, a)
		return cmd
	}
	msgViewAction(t.view, a)
	return nil
}

// openCellDoc is gd on a table's _id cell: that document's detail.
func (t *esConsoleTab) openCellDoc() tea.Cmd {
	c := t.cur()
	if c == nil || !c.asTable || c.table == nil {
		t.setStatus("no document here: show the hits as a table first", true)
		return nil
	}
	row, col := t.grid.Position()
	cols := c.table.Columns
	idCol, idxCol := slices.Index(cols, "_id"), slices.Index(cols, "_index")
	if row >= len(c.table.Rows) || idCol < 0 || idxCol < 0 || col != idCol {
		t.setStatus("put the cursor on an _id cell", true)
		return nil
	}
	r := c.table.Rows[row]
	msg := esOpenIDMsg{conn: t.conn, target: r[idxCol], kind: targetIndex, text: r[idCol]}
	return func() tea.Msg { return msg }
}

// yankText copies the result tab's request (or the one at the cursor)
// as curl; the grid's copies are left to the grid.
func (t *esConsoleTab) yankText(a keymap.Action) (text, what string, ok bool) {
	if a != keymap.YankCurl {
		return "", "", false
	}
	req, found := esx.Request{}, false
	if c := t.cur(); t.onResults && c != nil {
		req, found = c.req, true
	} else if reqs, err := esx.ParseConsole(t.editor.Value()); err == nil {
		line, _ := t.editor.Cursor()
		req, found = esx.RequestAt(reqs, line)
	}
	if !found {
		return "", "", false
	}
	cfg, _ := t.backend.Connection(t.conn)
	return curlFor(cfg, req.Method, catFormat(req.Path), req.Body), "the request as curl", true
}

// ---- running ----

func (t *esConsoleTab) parse() ([]esx.Request, bool) {
	reqs, err := esx.ParseConsole(t.editor.Value())
	var pe *esx.ParseError
	if errors.As(err, &pe) {
		t.editor.SetCursor(pe.Line, 0)
		t.onResults = false
		t.setStatus(err.Error()+" · nothing sent", true)
		return nil, false
	}
	if len(reqs) == 0 {
		t.setStatus("nothing to run", true)
		return nil, false
	}
	return reqs, true
}

func (t *esConsoleTab) runAtCursor() tea.Cmd {
	reqs, ok := t.parse()
	if !ok {
		return nil
	}
	if from, to, sel := t.editor.Selection(); sel {
		t.editor.Escape()
		text := t.editor.Value()
		fromLine, toLine := strings.Count(text[:min(from, len(text))], "\n"), strings.Count(text[:min(to, len(text))], "\n")
		var picked []esx.Request
		for _, r := range reqs {
			if r.End >= fromLine && r.Line <= toLine {
				picked = append(picked, r)
			}
		}
		return t.start(picked)
	}
	line, _ := t.editor.Cursor()
	r, found := esx.RequestAt(reqs, line)
	if !found {
		t.setStatus("nothing to run here", true)
		return nil
	}
	return t.start([]esx.Request{r})
}

// runAll runs the requests from line on.
func (t *esConsoleTab) runAll(line int) tea.Cmd {
	reqs, ok := t.parse()
	if !ok {
		return nil
	}
	if line > 0 {
		if r, found := esx.RequestAt(reqs, line); found {
			reqs = reqs[slices.IndexFunc(reqs, func(x esx.Request) bool { return x.Line == r.Line }):]
		}
	}
	return t.start(reqs)
}

// catFormat adds format=json to a _cat request that names no format.
func catFormat(path string) string {
	p, q, _ := strings.Cut(path, "?")
	if !strings.HasPrefix(strings.TrimLeft(p, "/"), "_cat/") && strings.TrimLeft(p, "/") != "_cat" {
		return path
	}
	for part := range strings.SplitSeq(q, "&") {
		if strings.HasPrefix(part, "format=") {
			return path
		}
	}
	if q == "" {
		return p + "?format=json"
	}
	return p + "?" + q + "&format=json"
}

// start grades the requests (spec §9.2): reads run at once; the writes
// are confirmed together; each dangerous request alone; the critical ones
// after their targets are expanded.
func (t *esConsoleTab) start(reqs []esx.Request) tea.Cmd {
	if t.running || len(reqs) == 0 {
		return nil
	}
	t.gen++
	run := &esRun{reqs: reqs}
	for _, r := range reqs {
		run.classes = append(run.classes, esx.Classify(r.Method, r.Path, r.Body))
	}
	t.run = run
	cfg, _ := t.backend.Connection(t.conn)
	var critical []int
	for i, c := range run.classes {
		switch {
		case c.Level == esx.LevelRead:
		case cfg.IsReadOnly():
			t.run = nil
			t.setStatus(readOnlyHint+" · nothing sent", true)
			return nil
		case c.Level == esx.LevelCritical && cfg.Env == config.EnvProd:
			t.run = nil
			t.editor.SetCursor(reqs[i].Line, 0)
			t.setStatus(esProdRefusal+" · nothing sent", true)
			return nil
		case c.Level == esx.LevelCritical && len(c.Targets) > 0 && pathTargets(reqs[i].Path):
			critical = append(critical, i)
		}
	}
	if len(critical) == 0 {
		return t.plan(nil)
	}
	b, conn, id, gen := t.backend, t.conn, t.id, t.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esResolvedMsg{tabID: id, conn: conn, gen: gen, resolved: map[int][]string{}}
		out.err = withES(ctx, b, conn, func(d es.Doer) error {
			for _, i := range critical {
				r, err := es.ResolveIndex(ctx, d, strings.Join(run.classes[i].Targets, ","))
				if err != nil {
					return err
				}
				out.resolved[i] = r.Indices
			}
			return nil
		})
		return out
	}
}

// pathTargets reports a request whose index expression is its path's
// first segment, so it can be sent with the expanded names instead.
func pathTargets(path string) bool {
	seg, _, _ := strings.Cut(strings.TrimLeft(path, "/"), "/")
	seg, _, _ = strings.Cut(seg, "?")
	return seg != "" && (!strings.HasPrefix(seg, "_") || seg == "_all")
}

// plan builds the run's confirmations once its targets are expanded.
func (t *esConsoleTab) plan(resolved map[int][]string) tea.Cmd {
	run := t.run
	run.resolved = resolved
	var writes []string
	for i, c := range run.classes {
		r := run.reqs[i]
		switch c.Level {
		case esx.LevelWrite:
			if !c.Quiet {
				writes = append(writes, r.Method+" "+r.Path)
			}
		case esx.LevelDanger, esx.LevelCritical:
			p := esPlan{Class: c, Resolved: resolved[i], Lines: []string{r.Method + " " + r.Path}, Method: r.Method, Path: r.Path, Body: r.Body}
			if len(c.Targets) == 0 {
				seg, _, _ := strings.Cut(strings.TrimLeft(r.Path, "/"), "/")
				p.Expect, _, _ = strings.Cut(seg, "?")
			}
			if c.Counts != nil {
				p.Lines = append(p.Lines, bulkCounts(c.Counts))
			}
			run.steps = append(run.steps, p)
		}
	}
	if writes != nil {
		first := slices.IndexFunc(run.classes, func(c esx.Class) bool { return c.Level == esx.LevelWrite && !c.Quiet })
		r := run.reqs[first]
		p := esPlan{
			Class: esx.Class{Level: esx.LevelWrite, Op: plural(len(writes), "write request")},
			Lines: writes, Method: r.Method, Path: r.Path, Body: r.Body,
		}
		run.steps = append([]esPlan{p}, run.steps...)
	}
	return t.step(0)
}

func bulkCounts(c map[string]int) string {
	var parts []string
	for _, k := range []string{"index", "create", "update", "delete"} {
		if c[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c[k], k))
		}
	}
	return "bulk: " + strings.Join(parts, " · ")
}

// step asks for confirmation i, or runs once they all passed.
func (t *esConsoleTab) step(i int) tea.Cmd {
	run := t.run
	if run == nil {
		return nil
	}
	if i >= len(run.steps) {
		return t.execute()
	}
	cfg, _ := t.backend.Connection(t.conn)
	return confirmESCmd(cfg, run.steps[i], esStepMsg{tabID: t.id, gen: t.gen, step: i + 1}, func(s string, bad bool) {
		t.run = nil
		t.setStatus(s+" · nothing sent", bad)
	})
}

// execute sends the run's requests in order, the critical ones to the
// indices their confirmation listed.
func (t *esConsoleTab) execute() tea.Cmd {
	run := t.run
	t.run = nil
	reqs := slices.Clone(run.reqs)
	for i, names := range run.resolved {
		rest := ""
		if _, after, ok := strings.Cut(strings.TrimLeft(reqs[i].Path, "/"), "/"); ok {
			rest = "/" + after
		}
		reqs[i].Path = strings.Join(names, ",") + rest
	}
	for i := range reqs {
		reqs[i].Path = catFormat(reqs[i].Path)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.running, t.cancel, t.started = true, cancel, time.Now()
	b, conn, id, gen, st := t.backend, t.conn, t.id, t.gen, t.store
	return tea.Batch(func() tea.Msg {
		defer cancel()
		out := esConsoleRunMsg{tabID: id, conn: conn, gen: gen}
		d, err := b.ES(ctx, conn)
		if err != nil {
			out.err = err
			return out
		}
		for _, r := range reqs {
			res := esResult{req: r}
			res.resp, res.err = d.Do(ctx, es.Request{Method: r.Method, Path: r.Path, Body: r.Body, NDJSON: r.NDJSON})
			out.results = append(out.results, res)
			recordRequest(st, conn, r, res)
			if ctx.Err() != nil {
				break
			}
		}
		return out
	}, t.tick(gen))
}

// recordRequest adds a request to the history, without the body of a
// request that carries a password.
func recordRequest(st Store, conn string, r esx.Request, res esResult) {
	if st == nil {
		return
	}
	text := r.Method + " " + r.Path
	if body := strings.TrimSpace(string(r.Body)); body != "" {
		if esx.SensitiveRequest(r.Method, r.Path) {
			body = "<redacted>"
		}
		text += "\n" + body
	}
	e := history.Entry{Time: time.Now(), Conn: conn, SQL: text, Duration: res.resp.Took}
	if res.err != nil {
		e.Err = res.err.Error()
	} else if err := es.CheckStatus(res.resp); err != nil {
		e.Err = err.Error()
	}
	if err := st.AddHistory(e); err != nil {
		slog.Warn("save history", "err", err)
	}
}

func (t *esConsoleTab) tick(gen int) tea.Cmd {
	id := t.id
	return tea.Tick(t.timings.tick, func(time.Time) tea.Msg { return runTickMsg{tabID: id, gen: gen} })
}

func (t *esConsoleTab) cancelRun() tea.Cmd {
	if !t.running {
		t.setStatus("no request is running · :qa quits", false)
		return nil
	}
	t.cancel()
	t.setStatus("canceling…", false)
	return nil
}

func (t *esConsoleTab) Update(msg tea.Msg) tea.Cmd {
	if cmd, ok := t.completeUpdate(msg); ok {
		return cmd
	}
	switch m := msg.(type) {
	case runTickMsg:
		if t.running && m.gen == t.gen {
			return t.tick(m.gen)
		}
	case esResolvedMsg:
		if m.gen != t.gen || t.run == nil {
			return nil
		}
		if m.err != nil {
			t.run = nil
			t.setStatus("cannot expand the targets: "+esReason(m.err)+" · nothing sent", true)
			return nil
		}
		return t.plan(m.resolved)
	case esStepMsg:
		if m.gen == t.gen {
			return t.step(m.step)
		}
	case esConsoleRunMsg:
		if m.gen != t.gen {
			return nil
		}
		t.running = false
		if m.err != nil {
			t.setStatus(esReason(m.err), true)
			return nil
		}
		t.done(m.results)
	case clipboardTextMsg:
		req := t.clipPaste
		t.clipPaste = nil
		if req != nil && m.text != "" && t.editor.PasteClipboard(*req, m.text) {
			return t.scheduleDraft()
		}
	case draftLoadedMsg:
		switch cur := t.editor.Value(); {
		case m.text == "" || m.text == cur:
		case cur == "":
			t.editor.SetValue(m.text)
		default:
			t.editor.SetValue(m.text + "\n" + cur)
		}
	case draftTickMsg:
		if m.gen == t.draftGen {
			save := t.saveDraft()
			return func() tea.Msg { save(); return nil }
		}
	case editFileMsg:
		if m.err != nil {
			t.setStatus("cannot create temp file: "+m.err.Error(), true)
			return nil
		}
		argv := append(slices.Clone(t.editorCmd), m.path)
		id, path := t.id, m.path
		return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg { //nolint:gosec // the user's own editor
			return editDoneMsg{tabID: id, path: path, err: err}
		})
	case editDoneMsg:
		return readEditedFile(t.id, m)
	case editTextMsg:
		if m.err != nil {
			t.setStatus("editor: "+m.err.Error()+" · text unchanged", true)
			return nil
		}
		t.editor.ReplaceAll(strings.TrimRight(m.text, "\n"))
		return t.scheduleDraft()
	}
	return nil
}

// done makes a result tab per request and shows the first failed one,
// else the last.
func (t *esConsoleTab) done(results []esResult) {
	labels, failed := make([]string, len(results)), make([]bool, len(results))
	pick := len(results)
	for i := range results {
		r := &results[i]
		r.lines = responseLines(*r)
		labels[i] = fmt.Sprintf("%d %s %d", i+1, r.req.Method, r.resp.Status)
		if r.err != nil {
			labels[i] = fmt.Sprintf("%d %s ✗", i+1, r.req.Method)
		}
		failed[i] = r.failed()
		if failed[i] && pick == len(results) {
			pick = i + 1
		}
		t.log(fmt.Sprintf("%s %s → %s", r.req.Method, r.req.Path, esOutcome(*r)))
	}
	t.results = results
	t.tabs.set(labels, failed)
	t.selectResult(pick)
	t.onResults = true
	t.setStatus("", false)
}

func esOutcome(r esResult) string {
	if r.err != nil {
		return esReason(r.err)
	}
	return fmt.Sprintf("%d · %s", r.resp.Status, r.resp.Took.Round(time.Millisecond))
}

// responseLines are a response as indented JSON, an error first as its
// type and reason; at most esJSONLines.
func responseLines(r esResult) []string {
	if r.err != nil {
		return []string{theme.Error.Render(esReason(r.err))}
	}
	var lines []string
	if err := es.CheckStatus(r.resp); err != nil {
		lines = append(lines, theme.Error.Render(textutil.Sanitize(err.Error())), "")
	}
	body := strings.Split(textutil.PrettyJSON(string(r.resp.Body)), "\n")
	if len(body) > esJSONLines {
		n := len(body)
		body = append(body[:esJSONLines], theme.Muted.Render(fmt.Sprintf("… %s more lines · show it as a table, export it or add size / filter_path", groupDigits(int64(n-esJSONLines)))))
	}
	return append(lines, body...)
}

func (t *esConsoleTab) cur() *esResult {
	if t.tabs.cur == 0 || t.tabs.cur > len(t.results) {
		return nil
	}
	return &t.results[t.tabs.cur-1]
}

func (t *esConsoleTab) selectResult(i int) {
	t.tabs.cur = i
	c := t.cur()
	if c == nil {
		t.view.SetLines(t.messages)
		return
	}
	if c.asTable && c.table != nil {
		t.grid.SetResult(textResult(c.table.Columns, c.table.Rows))
		return
	}
	t.view.SetLines(c.lines)
	t.view.Top()
}

// toggleView switches a response between its JSON and a table: hits,
// a _cat list or the first aggregation's buckets.
func (t *esConsoleTab) toggleView() {
	c := t.cur()
	if c == nil {
		return
	}
	if c.asTable {
		c.asTable = false
		t.selectResult(t.tabs.cur)
		return
	}
	if c.table == nil {
		for _, f := range []func([]byte) (esx.Table, error){esx.HitsTable, esx.CatTable, esx.AggTable} {
			if tb, err := f(c.resp.Body); err == nil {
				c.table = &tb
				break
			}
		}
	}
	if c.table == nil {
		t.setStatus("no table view for this response", true)
		return
	}
	c.asTable = true
	t.selectResult(t.tabs.cur)
}

func (t *esConsoleTab) retryAfterPassword() tea.Cmd { return nil }

func (t *esConsoleTab) shutdown() func() {
	cancel, save := t.cancel, t.saveDraft()
	return func() {
		if cancel != nil {
			cancel()
		}
		save()
	}
}

func (t *esConsoleTab) Init() tea.Cmd {
	id, st, key := t.id, t.store, t.Key()
	return func() tea.Msg {
		text, err := st.Draft(key)
		if err != nil {
			slog.Warn("load draft", "err", err)
		}
		return draftLoadedMsg{tabID: id, text: text}
	}
}

func (t *esConsoleTab) scheduleDraft() tea.Cmd {
	t.draftGen++
	id, gen := t.id, t.draftGen
	return tea.Tick(t.timings.draft, func(time.Time) tea.Msg { return draftTickMsg{tabID: id, gen: gen} })
}

func (t *esConsoleTab) saveDraft() func() {
	st, key, text := t.store, t.Key(), t.editor.Value()
	return func() {
		if err := st.SaveDraft(key, text); err != nil {
			slog.Warn("save draft", "err", err)
		}
	}
}

// ---- view ----

func (t *esConsoleTab) View(focused bool) string { return t.Framed("", focused, false) }

func (t *esConsoleTab) Framed(title string, focused, danger bool) string {
	if t.height == 0 || t.width == 0 {
		return ""
	}
	onEditor, onResults := focused && !t.onResults, focused && t.onResults
	bodyH := max(t.resultsH-1, 0)
	var body string
	switch c := t.cur(); {
	case c == nil:
		lines := t.messages[max(len(t.messages)-bodyH, 0):]
		body = textutil.PadLines(textutil.Sanitize(strings.Join(lines, "\n")), bodyH)
	case c.asTable:
		body = textutil.PadLines(t.grid.View(onResults), bodyH)
	default:
		body = textutil.PadLines(t.view.View(onResults), bodyH)
	}
	results := t.tabs.view(t.width) + "\n" + body
	t.editor.SetFocused(onEditor)
	switch {
	case t.full && t.onResults:
		return framedFull(title, t.resultsTitle(), results, t.statusLine(), t.width, t.height, onResults, danger)
	case t.full:
		return framedFull(title, "", t.editor.View(), t.statusLine(), t.width, t.height, onEditor, danger)
	}
	w := t.width + 2
	top := theme.Frame(title, t.editor.View(), w, t.editorH+2, onEditor, danger)
	bottom := theme.Frame(t.resultsTitle(), results+"\n"+t.statusLine(), w, t.resultsH+3, onResults, danger)
	view := top + "\n" + bottom
	if !t.CapturesInput() {
		return view
	}
	return placePopup(view, t.popup, t.editor, t.width, t.height)
}

func (t *esConsoleTab) resultsTitle() string {
	c := t.cur()
	switch {
	case c == nil:
		return "Summary"
	case c.err != nil:
		return "Error"
	case c.asTable:
		return fmt.Sprintf("%d · %s · %s rows", c.resp.Status, c.resp.Took.Round(time.Millisecond), groupDigits(int64(len(c.table.Rows))))
	}
	return fmt.Sprintf("%d · %s", c.resp.Status, c.resp.Took.Round(time.Millisecond))
}

func (t *esConsoleTab) statusLine() string {
	switch {
	case t.running:
		el := time.Since(t.started)
		frame := spinner[int(el/(100*time.Millisecond))%len(spinner)]
		return theme.Muted.Render(textutil.Fit(fmt.Sprintf("%s running %.1fs · ctrl+c cancel", frame, el.Seconds()), t.width))
	case t.editor.Pending() != "" && !t.onResults:
		return theme.Muted.Render(textutil.Fit(textutil.Sanitize(t.editor.Pending()), t.width))
	case t.status != "" && t.statusErr:
		return theme.Error.Render(textutil.Fit("✗ "+textutil.Sanitize(t.status), t.width))
	case t.status != "":
		return theme.Muted.Render(textutil.Fit(textutil.Sanitize(t.status), t.width))
	}
	hint := joinHints(
		keyHint(t.keys, keymap.Console, keymap.RunStatement, "run"),
		keyHint(t.keys, keymap.Console, keymap.RunAll, "run all"),
		keyHint(t.keys, keymap.Console, keymap.FocusResults, "results"),
		keyHint(t.keys, keymap.Console, keymap.ExternalEdit, "$EDITOR"),
		"METHOD path, then a JSON body · # comments",
	)
	if t.onResults {
		hint = joinHints(
			keyHint(t.keys, keymap.ESResults, keymap.ResultView, "JSON / table"),
			keyHint(t.keys, keymap.ESResults, keymap.NextResult, "result tabs"),
			keyHint(t.keys, keymap.ESResults, keymap.OpenCellKey, "open document"),
			keyHint(t.keys, keymap.ESResults, keymap.YankCurl, "curl"),
			keyHint(t.keys, keymap.ESResults, keymap.FocusEditor, "editor"))
	}
	return theme.Muted.Render(textutil.Fit(hint, t.width))
}
