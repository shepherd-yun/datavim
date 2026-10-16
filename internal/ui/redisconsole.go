package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/rcmd"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/session"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/editor"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/textview"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
	"github.com/shepherd-yun/sqlvim/internal/ui/vimedit"
)

// rconsoleTabKey is the tab (and draft) key of the redis console on db n.
func rconsoleTabKey(conn string, n int) string {
	return "rconsole\x00" + conn + "\x00" + strconv.Itoa(n)
}

// openRedisConsole opens, or switches to, the console on db n of a redis
// connection (n < 0 is the configured default).
func (m *Model) openRedisConsole(conn string, n int) tea.Cmd {
	if n < 0 {
		cfg, _ := m.backend.Connection(conn)
		n = config.RedisDB(cfg)
	}
	rec := history.Recent{Kind: history.RecentConsole, Conn: conn, Database: strconv.Itoa(n)}
	return m.openTab(rconsoleTabKey(conn, n), rec, func(id int) tab {
		t := newRedisConsoleTab(id, m.backend, m.store, conn, n, m.settings, m.timings)
		t.keys = m.keys
		return t
	})
}

// redisDBOf is the db number a redis node or tab is on; -1 when it has none
// (the connection node).
func redisDBOf(ns db.Namespace) int {
	n, err := strconv.Atoi(ns.Database)
	if err != nil {
		return -1
	}
	return n
}

type (
	redisRunMsg struct {
		tabID, gen  int
		conn        string
		session     rdb.Session // handed back to the tab; nil when it was lost
		db          int         // the session's db when the run ended
		results     []redisResult
		reconnected bool
		err         error // opening the session failed
		total       int   // commands asked for
	}
	redisResult struct {
		Line     rcmd.Line
		Args     []string
		DB       int // the db the command ran on
		Reply    rdb.Reply
		Duration time.Duration
		Err      error // the connection was lost or the command canceled
	}
)

func (m redisRunMsg) TabID() int { return m.tabID }

// failure reports only a failure to open the session (a password, an SSH
// prompt); a lost console connection is the console's to handle.
func (m redisRunMsg) failure() (string, error) { return m.conn, m.err }

// failed reports whether the command did not succeed.
func (r redisResult) failed() bool { return r.Err != nil || r.Reply.Kind == rdb.KindError }

// redisConsoleTab is the command console of a redis connection: one command
// a line, run on a dedicated connection where SELECT and MULTI stick.
type redisConsoleTab struct {
	id        int
	keys      *keymap.Keymap
	backend   Backend
	store     Store
	editorCmd []string
	draftGen  int
	conn      string
	openDB    int // the db the console was opened on: its key
	db        int // the session's current db
	maxRows   int
	timings   timings

	editor       *editor.Model
	grid         *grid.Model
	view         *textview.Model
	text         []string   // the text result on screen; nil when the grid shows a table
	table        *replyView // the table on screen
	onResults    bool
	showMessages bool
	messages     []string

	session    rdb.Session
	hadSession bool // a session was open before: a new one is a reconnect
	running    bool
	cancel     context.CancelFunc
	started    time.Time
	gen        int
	attempt    []rcmd.Line // the latest batch, kept to retry it once a password is entered
	runText    string      // the editor text the batch came from
	clipPaste  *vimedit.ClipboardPaste
	status     string
	statusErr  bool

	width, height, editorH int
}

func newRedisConsoleTab(id int, b Backend, st Store, conn string, n int, s config.Settings, tm timings) *redisConsoleTab {
	t := &redisConsoleTab{
		id: id, backend: b, store: st, conn: conn, openDB: n, db: n,
		maxRows: s.MaxRows, timings: tm, editorCmd: s.EditorCommand(os.Getenv),
		editor: editor.New(), grid: grid.New(nil, s.MaxColumnWidth), view: textview.New(), keys: keymap.Default(),
	}
	t.editor.EnterInsert() // a console opens ready to type
	return t
}

func (t *redisConsoleTab) ID() int     { return t.id }
func (t *redisConsoleTab) Key() string { return rconsoleTabKey(t.conn, t.openDB) }

func (t *redisConsoleTab) Title() string {
	return "redis:" + t.conn + "/db" + strconv.Itoa(t.db)
}

func (t *redisConsoleTab) Context() keymap.Context {
	if t.onResults {
		return keymap.RedisResults
	}
	return keymap.Console
}

func (t *redisConsoleTab) Target() (string, db.TableRef) {
	return t.conn, db.TableRef{Namespace: db.Namespace{Database: strconv.Itoa(t.db)}}
}

func (t *redisConsoleTab) CapturesInput() bool { return !t.onResults && t.editor.Insert() }

func (t *redisConsoleTab) Mode() string {
	if t.onResults {
		return "NORMAL"
	}
	return t.editor.Mode().String()
}

func (t *redisConsoleTab) Find(q string, forward bool) bool {
	found := false
	if t.text != nil {
		found = t.view.Find(q, forward)
	} else {
		found = t.grid.Find(q, forward)
	}
	if found {
		t.onResults = true
	}
	return found
}

func (t *redisConsoleTab) activeGrid() *grid.Model { return t.grid }

func (t *redisConsoleTab) exportSource() exportSrc {
	if t.text != nil {
		return exportSrc{}
	}
	return exportSrc{rs: t.grid.Result(), name: "redis", conn: t.conn}
}

// SetSize takes the size inside the tab area's frame; like the SQL console
// the tab draws its frames itself.
func (t *redisConsoleTab) SetSize(w, h int) {
	t.width, t.height = max(w, 0), max(h, 0)
	body := max(t.height-3, 0)
	t.editorH = min(max(body*2/5, 3), body)
	t.editor.SetSize(t.width, t.editorH)
	t.grid.SetSize(t.width, body-t.editorH)
	t.view.SetSize(t.width, body-t.editorH)
}

func (t *redisConsoleTab) setStatus(s string, isErr bool) { t.status, t.statusErr = s, isErr }

func (t *redisConsoleTab) log(line string) {
	t.messages = append(t.messages, line)
	if over := len(t.messages) - maxMessages; over > 0 {
		t.messages = t.messages[over:]
	}
}

// ---- input ----

func (t *redisConsoleTab) Input(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "ctrl+enter", "ctrl+j":
			return t.runAtCursor()
		case "ctrl+c":
			return t.cancelRun()
		}
	}
	before := t.editor.Version()
	cmd := t.effects(t.editor.Update(msg))
	if t.editor.Version() == before {
		return cmd
	}
	return tea.Batch(cmd, t.scheduleDraft())
}

func (t *redisConsoleTab) Paste(msg tea.PasteMsg) tea.Cmd {
	if t.onResults {
		return nil
	}
	if t.editor.Insert() {
		return t.Input(msg)
	}
	before := t.editor.Version()
	t.editor.Update(msg)
	if t.editor.Version() == before {
		return nil
	}
	return t.scheduleDraft()
}

func (t *redisConsoleTab) TakeKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
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
	cmd := t.effects(res)
	if t.editor.Version() != before {
		cmd = tea.Batch(cmd, t.scheduleDraft())
	}
	return cmd, true
}

// effects carries out the editor's clipboard and tab-jump requests.
func (t *redisConsoleTab) effects(res vimedit.Result) tea.Cmd {
	return editorEffects(t.id, res, &t.clipPaste)
}

func (t *redisConsoleTab) Handle(a keymap.Action) tea.Cmd {
	switch a {
	case keymap.Cancel:
		return t.cancelRun()
	case keymap.FocusResults:
		t.onResults = true
		return nil
	case keymap.FocusEditor:
		t.onResults = false
		return nil
	case keymap.ToggleMessages:
		t.showMessages = !t.showMessages
		return nil
	}
	if t.onResults {
		return t.resultsAction(a)
	}
	switch a {
	case keymap.ExternalEdit:
		return t.externalEdit()
	case keymap.RunStatement:
		return t.runAtCursor()
	case keymap.RunAll:
		return t.run(rcmd.Lines(t.editor.Value()))
	case keymap.RunFromCursor:
		return t.run(rcmd.From(rcmd.Lines(t.editor.Value()), t.editor.Offset()))
	}
	return nil
}

func (t *redisConsoleTab) resultsAction(a keymap.Action) tea.Cmd {
	if t.text == nil {
		cmd, _ := gridAction(t.grid, a)
		return cmd
	}
	switch a {
	case keymap.Down:
		t.view.Move(1)
	case keymap.Up:
		t.view.Move(-1)
	case keymap.Top:
		t.view.Top()
	case keymap.Bottom:
		t.view.Bottom()
	case keymap.HalfPageDown:
		t.view.HalfPage(1)
	case keymap.HalfPageUp:
		t.view.HalfPage(-1)
	}
	return nil
}

// ---- running ----

func (t *redisConsoleTab) runAtCursor() tea.Cmd {
	lines := rcmd.Lines(t.editor.Value())
	if from, to, ok := t.editor.Selection(); ok {
		t.editor.Escape()
		return t.run(rcmd.Within(lines, from, to))
	}
	l, ok := rcmd.At(lines, t.editor.Offset())
	if !ok {
		t.setStatus("nothing to run", true)
		return nil
	}
	return t.run([]rcmd.Line{l})
}

func (t *redisConsoleTab) run(lines []rcmd.Line) tea.Cmd {
	if t.running {
		t.setStatus("a command is already running · ctrl+c to cancel", true)
		return nil
	}
	if len(lines) == 0 {
		t.setStatus("nothing to run", true)
		return nil
	}
	t.runText = t.editor.Value()
	for _, l := range lines {
		if _, err := rcmd.Split(l.Text); err != nil {
			return t.refuse(l, splitStatus(l, err))
		}
	}
	return t.execute(lines)
}

func splitStatus(l rcmd.Line, err error) string {
	var se *rcmd.SplitError
	if errors.As(err, &se) {
		return fmt.Sprintf("line %d, col %d: %s", l.Num, se.Col, se.Msg)
	}
	return err.Error()
}

// refuse reports a batch that does not run because of line l.
func (t *redisConsoleTab) refuse(l rcmd.Line, reason string) tea.Cmd {
	t.log("✗ " + oneLine(l.Text) + " — " + reason)
	t.setStatus(reason, true)
	t.gotoLine(l)
	return nil
}

func (t *redisConsoleTab) gotoLine(l rcmd.Line) {
	if t.runText == t.editor.Value() {
		t.onResults = false
		t.editor.SetCursorOffset(l.Start + len(l.Text) - len(strings.TrimLeft(l.Text, " \t")))
	}
}

func (t *redisConsoleTab) execute(lines []rcmd.Line) tea.Cmd {
	t.attempt = lines
	ctx, cancel := context.WithCancel(context.Background())
	t.gen++
	t.running, t.cancel, t.started = true, cancel, time.Now()
	t.setStatus("", false)
	gen, id, b, conn := t.gen, t.id, t.backend, t.conn
	s, n, had := t.session, t.db, t.hadSession
	t.session = nil // owned by the command until redisRunMsg hands it back
	run := func() tea.Msg {
		defer cancel()
		msg := redisRunMsg{tabID: id, gen: gen, conn: conn, db: n, total: len(lines)}
		if s != nil {
			pctx, pcancel := context.WithTimeout(ctx, pingTimeout)
			err := s.Ping(pctx)
			pcancel()
			if err != nil {
				slog.Info("redis console session lost", "conn", conn, "err", err)
				_ = s.Close()
				s = nil
			}
		}
		if s == nil {
			octx, ocancel := context.WithTimeout(ctx, loadTimeout)
			var err error
			s, err = b.RedisSession(octx, conn, n)
			ocancel()
			if err != nil {
				msg.err = err
				return msg
			}
			msg.reconnected = had
		}
		for _, l := range lines {
			args, _ := rcmd.Split(l.Text)
			r := redisResult{Line: l, Args: args, DB: s.DB()}
			began := time.Now()
			r.Reply, r.Err = s.Do(ctx, args...)
			r.Duration = time.Since(began)
			msg.results = append(msg.results, r)
			msg.db = s.DB()
			if r.Err != nil {
				_ = s.Close()
				s = nil
				break
			}
			if r.Reply.Kind == rdb.KindError {
				break
			}
		}
		msg.session = s
		return msg
	}
	return tea.Batch(run, t.tick(gen))
}

func (t *redisConsoleTab) tick(gen int) tea.Cmd {
	id := t.id
	return tea.Tick(t.timings.tick, func(time.Time) tea.Msg { return runTickMsg{tabID: id, gen: gen} })
}

func (t *redisConsoleTab) cancelRun() tea.Cmd {
	if !t.running {
		t.setStatus("no command is running · :qa quits", false)
		return nil
	}
	t.cancel()
	t.setStatus("canceling…", false)
	return nil
}

func (t *redisConsoleTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case runTickMsg:
		if t.running && m.gen == t.gen {
			return t.tick(m.gen)
		}
	case redisRunMsg:
		return t.done(m)
	case clipboardTextMsg:
		req := t.clipPaste
		t.clipPaste = nil
		switch {
		case req == nil:
		case m.text == "":
			t.setStatus("the clipboard is empty or could not be read", true)
		case !t.editor.PasteClipboard(*req, m.text):
			t.setStatus("the text changed before the clipboard arrived · paste again", true)
		default:
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
		return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg {
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
		t.setStatus("updated from "+t.editorCmd[0], false)
		return t.scheduleDraft()
	}
	return nil
}

func (t *redisConsoleTab) done(m redisRunMsg) tea.Cmd {
	if m.gen != t.gen {
		if m.session != nil {
			s := m.session
			return func() tea.Msg { _ = s.Close(); return nil }
		}
		return nil
	}
	t.running, t.cancel = false, nil
	t.session, t.db = m.session, m.db
	if m.err != nil {
		text := m.err.Error()
		if errors.Is(m.err, session.ErrPasswordRequired) {
			text = "password required for " + t.conn
		}
		t.log("✗ " + text)
		t.setStatus(text, true)
		t.showMessages = true
		return nil
	}
	t.hadSession = true
	if m.reconnected {
		t.log(fmt.Sprintf("reconnected (db %d)", t.db))
	}
	var total time.Duration
	var shown *replyView
	var text []string
	for _, r := range m.results {
		total += r.Duration
		t.log(describeRedis(r, t.maxRows))
		if r.Err != nil {
			continue
		}
		v := viewReply(r.Args, r.Reply, t.maxRows)
		if v.Columns != nil {
			shown, text = &v, nil
		} else if shown == nil {
			text = v.Lines
		}
	}
	switch {
	case shown != nil:
		t.table, t.text = shown, nil
		t.grid.SetResult(shown.resultSet())
		t.showMessages = false
	case text != nil:
		t.table, t.text = nil, text
		t.view.SetLines(text)
		t.view.Top()
		t.showMessages = false
	}
	if len(m.results) == 0 {
		t.setStatus("canceled", true)
		return nil
	}
	last := m.results[len(m.results)-1]
	switch {
	case last.failed():
		status := failText(last)
		if m.total > 1 {
			status = fmt.Sprintf("command %d/%d failed: %s", len(m.results), m.total, status)
		}
		t.setStatus(status, true)
		t.gotoLine(last.Line)
		if last.Err != nil {
			t.showMessages = true
		}
	default:
		t.setStatus(fmt.Sprintf("%d command(s) ok in %s", len(m.results), total.Round(time.Millisecond)), false)
	}
	return t.record(m.results)
}

func failText(r redisResult) string {
	if r.Err != nil {
		return redisErrorText(r.Err)
	}
	return r.Reply.Str
}

func redisErrorText(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case db.IsConnLost(err):
		return "connection lost — the command may or may not have run; the next run reconnects"
	}
	return err.Error()
}

// describeRedis is a result's line in the message log.
func describeRedis(r redisResult, maxRows int) string {
	cmd := oneLine(rcmd.Redact(r.Args))
	if r.Err != nil {
		return "✗ " + cmd + " — " + redisErrorText(r.Err)
	}
	v := viewReply(r.Args, r.Reply, maxRows)
	mark := "✓ "
	if r.Reply.Kind == rdb.KindError {
		mark = "✗ "
	}
	s := fmt.Sprintf("%s%s — %s in %s", mark, cmd, v.Summary, r.Duration.Round(time.Millisecond))
	if v.Truncated {
		s += " (truncated)"
	}
	return s
}

func (t *redisConsoleTab) record(results []redisResult) tea.Cmd {
	st, now := t.store, time.Now()
	entries := make([]history.Entry, len(results))
	for i, r := range results {
		entries[i] = history.Entry{Time: now, Conn: t.conn, Database: strconv.Itoa(r.DB), SQL: rcmd.Redact(r.Args), Duration: r.Duration}
		if r.failed() {
			entries[i].Err = failText(r)
		}
	}
	return func() tea.Msg {
		for _, e := range entries {
			if err := st.AddHistory(e); err != nil {
				slog.Warn("save history", "err", err)
				break
			}
		}
		return nil
	}
}

// retryAfterPassword runs the latest batch again once a password is entered.
func (t *redisConsoleTab) retryAfterPassword() tea.Cmd {
	if t.attempt == nil || t.running {
		return nil
	}
	return t.execute(t.attempt)
}

func (t *redisConsoleTab) shutdown() func() {
	s, cancel, save := t.session, t.cancel, t.saveDraft()
	t.session = nil
	return func() {
		if cancel != nil {
			cancel()
		}
		if s != nil {
			_ = s.Close()
		}
		save()
	}
}

func (t *redisConsoleTab) Init() tea.Cmd {
	id, st, key := t.id, t.store, t.Key()
	return func() tea.Msg {
		text, err := st.Draft(key)
		if err != nil {
			slog.Warn("load draft", "err", err)
		}
		return draftLoadedMsg{tabID: id, text: text}
	}
}

func (t *redisConsoleTab) scheduleDraft() tea.Cmd {
	t.draftGen++
	id, gen := t.id, t.draftGen
	return tea.Tick(t.timings.draft, func(time.Time) tea.Msg { return draftTickMsg{tabID: id, gen: gen} })
}

func (t *redisConsoleTab) saveDraft() func() {
	st, key, text := t.store, t.Key(), t.editor.Value()
	return func() {
		if err := st.SaveDraft(key, text); err != nil {
			slog.Warn("save draft", "err", err)
		}
	}
}

// insertText appends a command to the editor (or fills an empty editor).
func (t *redisConsoleTab) insertText(s string) tea.Cmd {
	if cur := t.editor.Value(); cur == "" {
		t.editor.ReplaceAll(s)
	} else {
		t.editor.ReplaceAll(cur + "\n" + s)
	}
	t.onResults = false
	return t.scheduleDraft()
}

func (t *redisConsoleTab) externalEdit() tea.Cmd {
	return writeTempEdit(t.id, t.editor.Value(), "sqlvim-*.redis")
}

// ---- view ----

func (t *redisConsoleTab) View(focused bool) string { return t.Framed("", focused, false) }

func (t *redisConsoleTab) Framed(title string, focused, danger bool) string {
	if t.height == 0 || t.width == 0 {
		return ""
	}
	onEditor, onResults := focused && !t.onResults, focused && t.onResults
	resultsH := max(t.height-3, 0) - t.editorH
	var results string
	switch {
	case t.showMessages:
		results = t.messagesView(resultsH)
	case t.text != nil:
		results = textutil.PadLines(t.view.View(onResults), resultsH)
	default:
		results = textutil.PadLines(t.grid.View(onResults), resultsH)
	}
	t.editor.SetFocused(onEditor)
	w := t.width + 2
	top := theme.Frame(title, t.editor.View(), w, t.editorH+2, onEditor, danger)
	bottom := theme.Frame(t.resultsTitle(), results+"\n"+t.statusLine(), w, resultsH+3, onResults, danger)
	return top + "\n" + bottom
}

func (t *redisConsoleTab) resultsTitle() string {
	switch {
	case t.showMessages:
		return "Messages"
	case t.table != nil:
		label := fmt.Sprintf("Result · %d rows", len(t.table.Rows))
		if t.table.Truncated {
			label += " · truncated"
		}
		return label
	}
	return "Result"
}

func (t *redisConsoleTab) messagesView(h int) string {
	start := max(len(t.messages)-h, 0)
	lines := make([]string, 0, h)
	for _, m := range t.messages[start:] {
		lines = append(lines, textutil.Fit(textutil.Sanitize(m), t.width))
	}
	return textutil.PadLines(strings.Join(lines, "\n"), h)
}

func (t *redisConsoleTab) statusLine() string {
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
		keyHint(t.keys, keymap.Console, keymap.RunFromCursor, "run from here"),
		"one command a line · i insert · v select",
	)
	switch {
	case t.onResults:
		hint = joinHints(
			keyHint(t.keys, keymap.RedisResults, keymap.ToggleMessages, "messages"),
			keyHint(t.keys, keymap.RedisResults, keymap.FocusEditor, "editor"),
			"hjkl move · yy copy row · K show cell · / search")
	case t.editor.Visual():
		hint = "enter runs the selected lines · y d c > < · esc cancel"
	}
	return theme.Muted.Render(textutil.Fit(hint, t.width))
}
