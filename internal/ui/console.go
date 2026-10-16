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
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/meta"
	"github.com/shepherd-yun/sqlvim/internal/runner"
	"github.com/shepherd-yun/sqlvim/internal/session"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/highlight"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/complete"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/editor"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
	"github.com/shepherd-yun/sqlvim/internal/ui/vimedit"
)

const (
	pingTimeout = 5 * time.Second
	maxMessages = 500
)

// timings are the console's clocks; tests set them to zero.
type timings struct {
	tick  time.Duration // spinner refresh while a query runs
	draft time.Duration // idle time before a draft is saved
}

var defaultTimings = timings{tick: 100 * time.Millisecond, draft: time.Second}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Optional tab capabilities, checked by the root model with type assertions.
type (
	inputCapturer interface {
		CapturesInput() bool
		Input(msg tea.Msg) tea.Cmd
	}
	moder  interface{ Mode() string }
	finder interface {
		Find(q string, forward bool) bool
	}
	canceler interface{ cancelRun() tea.Cmd }
	keyTaker interface {
		TakeKey(msg tea.KeyPressMsg) (tea.Cmd, bool)
	}
	shutdowner interface{ shutdown() func() }
)

type runDoneMsg struct {
	tabID       int
	conn        string
	gen         int
	session     db.Session
	results     []runner.Result
	reconnected bool
	err         error // opening the session failed
	more        bool  // re-run of the shown statement with a higher limit
	limit       int
	readOnly    bool          // the connection was read-only when the run started
	reopened    bool          // the session was reopened because readonly changed
	began       bool          // a BEGIN was injected before the statements (manual commit)
	text        string        // the editor text the statements came from; "" if they did not
	filter      *resultFilter // the run reads the shown result again, narrowed by this
	note        string        // the status once the run succeeds
	total       int           // statements asked for, without an injected BEGIN
}

type runTickMsg struct{ tabID, gen int }

type confirmRunMsg struct{ tabID, gen int }

func (m runDoneMsg) TabID() int    { return m.tabID }
func (m runTickMsg) TabID() int    { return m.tabID }
func (m confirmRunMsg) TabID() int { return m.tabID }

func consoleTabKey(conn string, ns db.Namespace) string {
	return strings.Join([]string{"console", conn, ns.Database, ns.Schema}, "\x00")
}

type consoleTab struct {
	id         int
	scratchKey string       // a script console's own tab key; such a console keeps no draft
	file       *consoleFile // the .sql file this console edits; nil for a plain console
	keys       *keymap.Keymap
	backend    Backend
	store      Store
	editorCmd  []string
	draftGen   int
	conn       string
	ns         db.Namespace
	dialect    sqlx.Dialect
	maxRows    int
	timings    timings
	catalogs   *meta.Cache // shared with the root model; UI goroutine only
	metaKey    meta.Key
	defNS      db.Namespace    // where unqualified table names resolve
	popup      *complete.Model // completion popup; nil when closed
	popStart   int             // byte offset the popup's word starts at

	editor       *editor.Model
	grid         *grid.Model
	onResults    bool
	showMessages bool
	messages     []string

	res       *editableResult            // the result on screen, with its staged changes
	shown     resultFilter               // the statement and filter of the result on screen
	runFilter *resultFilter              // set before execute: the run reads the shown result again
	runNote   string                     // set before execute: the status once the run succeeds
	metas     map[db.TableRef]*tableMeta // keys of tables behind results; dropped when DDL runs

	session   db.Session
	sessionRO bool // the connection was read-only when session was opened
	manualTx  bool // <space>t: statements run in a transaction until :commit / :rollback
	inTx      bool // the session has an open transaction
	txWrites  int  // writes run in the open transaction
	txAborted bool // PostgreSQL: a statement failed, only ROLLBACK works now
	running   bool
	cancel    context.CancelFunc
	started   time.Time
	gen       int
	pending   []sqlx.Statement // waiting for confirmation
	last      *runner.Result   // the result set on screen
	lastLimit int
	runText   string                  // editor text of the run being started; "" for COMMIT, ROLLBACK and load-more
	attempt   *runAttempt             // the latest run, kept to retry it once a password is entered
	clipPaste *vimedit.ClipboardPaste // a "+p waiting for the clipboard
	status    string
	statusErr bool

	width, height, editorH int
}

func newConsoleTab(id int, b Backend, st Store, catalogs *meta.Cache, conn string, ns db.Namespace, s config.Settings, tm timings) *consoleTab {
	cfg, _ := b.Connection(conn)
	key, _ := metaKeyFor(b, conn, ns.Database)
	d := sqlx.DialectOf(cfg.Driver)
	t := &consoleTab{
		id: id, backend: b, store: st, conn: conn, ns: ns, dialect: d,
		maxRows: s.MaxRows, timings: tm, editorCmd: s.EditorCommand(os.Getenv),
		catalogs: catalogs, metaKey: key, defNS: defaultNamespace(cfg, key, ns),
		editor: editor.New(), grid: grid.New(nil, s.MaxColumnWidth), keys: keymap.Default(),
	}
	t.editor.SetHighlighter(highlight.NewHighlighter(d).Spans)
	t.editor.EnterInsert() // a console opens ready to type
	t.res = &editableResult{
		id: id, backend: b, conn: conn, dialect: d, keys: t.keys, keyCtx: keymap.Results,
		grid: t.grid, host: t, readOnly: "run a query first",
	}
	t.grid.SetMarks(t.res.mark)
	return t
}

func (t *consoleTab) ID() int { return t.id }
func (t *consoleTab) Key() string {
	if t.file != nil {
		return fileTabKey(t.file.path, t.conn, t.ns)
	}
	if t.scratchKey != "" {
		return t.scratchKey
	}
	return consoleTabKey(t.conn, t.ns)
}

func (t *consoleTab) Title() string {
	if t.file != nil {
		switch {
		case t.file.isNew:
			return t.file.name() + " [new]"
		case t.dirty():
			return t.file.name() + " [+]"
		}
		return t.file.name()
	}
	prefix := "console:"
	if t.scratchKey != "" {
		prefix = "sync:"
	}
	if t.ns.Database != "" {
		return prefix + t.ns.Database
	}
	return prefix + t.conn
}

// dirty reports a file console whose text differs from the file's.
func (t *consoleTab) dirty() bool { return t.file != nil && t.editor.Value() != t.file.saved }

// save writes the editor text to path, or to the console's file when path
// is "". quit closes the tab once written.
func (t *consoleTab) save(path string, force, quit bool) tea.Cmd {
	var known *consoleFile
	if t.file != nil && (path == "" || path == t.file.path) {
		f := *t.file
		known, path = &f, f.path
	}
	if path == "" {
		t.setStatus("no file name · :w <path>", true)
		return nil
	}
	id, text := t.id, t.editor.Value()
	return func() tea.Msg {
		f, conflict, err := writeSQLFile(path, text, known, force)
		return fileSavedMsg{tabID: id, file: f, conflict: conflict, quit: quit, err: err}
	}
}

func (t *consoleTab) saved(m fileSavedMsg) tea.Cmd {
	switch {
	case m.err != nil:
		t.setStatus(m.err.Error(), true)
		return nil
	case m.conflict != "":
		ok := saveConfirmedMsg{tabID: t.id, path: m.file.path, quit: m.quit}
		return openOverlay(overlay.NewConfirm("Write file", []string{m.conflict}, "", ok))
	}
	f := m.file
	t.file = &f
	t.setStatus(fmt.Sprintf("%q written, %s", tildePath(f.path), plural(lineCount(f.saved), "line")), false)
	if m.quit {
		id := t.id
		return func() tea.Msg { return closeTabConfirmedMsg{tabID: id} }
	}
	return nil
}

func (t *consoleTab) Context() keymap.Context {
	if t.onResults {
		return keymap.Results
	}
	return keymap.Console
}

func (t *consoleTab) Target() (string, db.TableRef) { return t.conn, db.TableRef{Namespace: t.ns} }

func (t *consoleTab) CapturesInput() bool { return !t.onResults && t.editor.Insert() }

func (t *consoleTab) Mode() string {
	if t.onResults {
		return "NORMAL"
	}
	return t.editor.Mode().String()
}

func (t *consoleTab) Find(q string, forward bool) bool {
	if t.grid.Find(q, forward) {
		t.onResults = true
		return true
	}
	return false
}

// SetSize takes the size inside the tab area's frame. The console draws
// that frame itself, split in two (see Framed), so it fills w+2 x h+2.
func (t *consoleTab) SetSize(w, h int) {
	t.width, t.height = max(w, 0), max(h, 0)
	body := max(t.height-3, 0) // the extra frame border pair and the status line
	t.editorH = min(max(body*2/5, 3), body)
	t.editor.SetSize(t.width, t.editorH)
	t.grid.SetSize(t.width, body-t.editorH)
}

func (t *consoleTab) setStatus(s string, isErr bool) { t.status, t.statusErr = s, isErr }

func (t *consoleTab) log(line string) {
	t.messages = append(t.messages, line)
	if over := len(t.messages) - maxMessages; over > 0 {
		t.messages = t.messages[over:]
	}
}

// ---- input ----

func (t *consoleTab) Input(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if cmd, used := t.popupKey(k.String()); used {
			return cmd
		}
		switch k.String() {
		case "ctrl+enter", "ctrl+j":
			t.popup = nil
			return t.runAtCursor()
		case "ctrl+c":
			return t.cancelRun()
		}
	}
	before := t.editor.Version()
	cmd := t.effects(t.editor.Update(msg))
	if t.editor.Version() == before || !t.editor.Insert() {
		t.popup = nil // the cursor moved, or esc left INSERT mode
		return cmd
	}
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "space" {
		t.suggestNext()
	} else {
		t.suggest(false)
	}
	return tea.Batch(cmd, t.scheduleDraft())
}

// Paste puts a terminal paste into the editor, in any mode; the results
// grid ignores it.
func (t *consoleTab) Paste(msg tea.PasteMsg) tea.Cmd {
	if t.onResults {
		return nil
	}
	if t.editor.Insert() {
		return t.Input(msg)
	}
	before := t.editor.Version()
	t.popup = nil
	t.editor.Update(msg)
	if t.editor.Version() == before {
		return nil
	}
	return t.scheduleDraft()
}

// TakeKey gives a key pressed in NORMAL or VISUAL mode to the editor
// before the keymap sees it; used is false when the editor leaves the key
// to the keymap (space, :, enter, ctrl+j …).
func (t *consoleTab) TakeKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if t.onResults {
		return nil, false
	}
	// The leader belongs to the keymap outside INSERT mode unless the editor
	// is mid-command ("f," finds a comma).
	if msg.String() == t.keys.Leader() && !t.editor.Insert() && t.editor.Pending() == "" {
		return nil, false
	}
	before := t.editor.Version()
	res := t.editor.Update(msg)
	if !res.Handled {
		return nil, false
	}
	t.popup = nil
	cmd := t.effects(res)
	if t.editor.Version() != before {
		cmd = tea.Batch(cmd, t.scheduleDraft())
	}
	return cmd, true
}

// effects carries out what the editor asked of the console: the system
// clipboard for "+ and tab jumps for gt / gT.
func (t *consoleTab) effects(res vimedit.Result) tea.Cmd {
	var cmds []tea.Cmd
	if res.Clipboard != nil {
		cmds = append(cmds, copyToClipboard(*res.Clipboard))
	}
	if res.Paste != nil {
		req, id := *res.Paste, t.id
		t.clipPaste = &req
		cmds = append(cmds, readClipboard(func(text string) tea.Msg {
			return clipboardTextMsg{tabID: id, text: text}
		}))
	}
	if res.Tab != nil {
		j := *res.Tab
		cmds = append(cmds, func() tea.Msg { return tabJumpMsg(j) })
	}
	return tea.Batch(cmds...)
}

func (t *consoleTab) Handle(a keymap.Action) tea.Cmd {
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
		if cmd, ok := gridAction(t.grid, a); ok {
			return cmd
		}
		if cmd, ok := t.res.handle(a); ok {
			return cmd
		}
		if a == keymap.LoadMore {
			return t.loadMore()
		}
		return nil
	}
	return t.editorAction(a)
}

// editorAction runs the keymap's console actions; vimedit handles the
// editing keys before the keymap sees them (TakeKey).
func (t *consoleTab) editorAction(a keymap.Action) tea.Cmd {
	switch a {
	case keymap.ExternalEdit:
		return t.externalEdit()
	case keymap.RunStatement:
		return t.runAtCursor()
	case keymap.RunAll:
		return t.run(sqlx.Split(t.editor.Value(), t.dialect))
	case keymap.RunFromCursor:
		return t.run(sqlx.From(sqlx.Split(t.editor.Value(), t.dialect), t.editor.Offset()))
	}
	return nil
}

// errTxLost reports a connection that dropped during a transaction: the
// server rolled the transaction back, so the console neither retries nor
// reconnects until the user runs again.
var errTxLost = errors.New("transaction lost: the connection dropped and the server rolled back its uncommitted changes · run again to reconnect")

// toggleTx switches between autocommit and manual commit.
func (t *consoleTab) toggleTx() tea.Cmd {
	if t.manualTx && t.inTx {
		t.setStatus("a transaction is open · :commit or :rollback first", true)
		return nil
	}
	t.manualTx = !t.manualTx
	if t.manualTx {
		t.setStatus("manual commit: statements run in a transaction until :commit or :rollback", false)
	} else {
		t.setStatus("autocommit", false)
	}
	return nil
}

// endTx runs COMMIT or ROLLBACK for :commit and :rollback.
func (t *consoleTab) endTx(commit bool) tea.Cmd {
	switch {
	case t.running:
		t.setStatus("a query is already running · ctrl+c to cancel", true)
	case !t.inTx:
		t.setStatus("no open transaction", true)
	case commit && t.txAborted:
		t.setStatus("the transaction failed and cannot be committed · :rollback", true)
	default:
		sql := "ROLLBACK"
		if commit {
			sql = "COMMIT"
		}
		t.runText = ""
		return t.execute([]sqlx.Statement{{Text: sql}}, t.maxRows, false)
	}
	return nil
}

// TxStatus is the status bar badge for the console's transaction.
func (t *consoleTab) TxStatus() string {
	switch {
	case t.txAborted:
		return "TX failed · :rollback"
	case t.inTx:
		return fmt.Sprintf("TX %d", t.txWrites)
	case t.manualTx:
		return "TX"
	}
	return ""
}

func (t *consoleTab) openTx() bool { return t.inTx }

func (t *consoleTab) clearTx() { t.inTx, t.txWrites, t.txAborted = false, 0, false }

// trackTx follows the transaction through the statements that ran.
// sessionLost means the session died during the run.
func (t *consoleTab) trackTx(results []runner.Result, sessionLost bool) {
	for _, r := range results {
		if r.Err != nil {
			// PostgreSQL refuses everything but ROLLBACK after an error in a transaction.
			if t.inTx && t.dialect == sqlx.Postgres && !db.IsConnLost(r.Err) {
				t.txAborted = true
			}
			continue
		}
		switch sqlx.TransactionEffect(r.SQL, t.dialect) {
		case sqlx.TxBegin:
			t.inTx, t.txWrites, t.txAborted = true, 0, false
			continue
		case sqlx.TxEnd:
			t.clearTx()
			continue
		case sqlx.TxResume:
			t.txAborted = false
			continue
		}
		if !t.inTx {
			continue
		}
		switch sqlx.Analyze(r.SQL, t.dialect).Kind {
		case sqlx.Write:
			t.txWrites++
		case sqlx.DDL:
			if t.dialect == sqlx.MySQL {
				t.log("MySQL committed the open transaction implicitly before " + oneLine(r.SQL))
				t.clearTx()
			} else {
				t.txWrites++
			}
		}
	}
	if sessionLost && t.inTx {
		t.log("✗ " + errTxLost.Error())
		t.clearTx()
	}
}

// ---- running ----

func (t *consoleTab) runAtCursor() tea.Cmd {
	if from, to, ok := t.editor.Selection(); ok {
		t.editor.Escape()
		return t.run(selectedStatements(t.editor.Value(), from, to, t.dialect))
	}
	s, ok := sqlx.At(sqlx.Split(t.editor.Value(), t.dialect), t.editor.Offset())
	if !ok {
		t.setStatus("nothing to run", true)
		return nil
	}
	return t.run([]sqlx.Statement{s})
}

// selectedStatements splits the selected text [from, to) of text into
// statements whose offsets point into text, so a failed statement's error
// position still lands in the editor.
func selectedStatements(text string, from, to int, d sqlx.Dialect) []sqlx.Statement {
	stmts := sqlx.Split(text[from:to], d)
	for i := range stmts {
		stmts[i].Start += from
		stmts[i].End += from
	}
	return stmts
}

func (t *consoleTab) run(stmts []sqlx.Statement) tea.Cmd {
	if t.res.changes() > 0 {
		t.setStatus(t.res.unsavedNote(), true)
		return nil
	}
	if t.running {
		t.setStatus("a query is already running · ctrl+c to cancel", true)
		return nil
	}
	if len(stmts) == 0 {
		t.setStatus("nothing to run", true)
		return nil
	}
	t.runText = t.editor.Value()
	cfg, _ := t.backend.Connection(t.conn)
	policy := runner.Policy{ReadOnly: cfg.IsReadOnly(), Prod: cfg.Env == config.EnvProd}
	switch chk := runner.Guard(stmts, t.dialect, policy); chk.Verdict {
	case runner.Reject:
		t.log("✗ " + oneLine(chk.SQL) + " — " + chk.Reason)
		t.setStatus(chk.Reason, true)
		return nil
	case runner.Confirm:
		t.gen++
		t.pending = stmts
		return openOverlay(overlay.NewConfirm(chk.Title, chk.Body, chk.Expect, confirmRunMsg{tabID: t.id, gen: t.gen}))
	}
	return t.execute(stmts, t.maxRows, false)
}

func (t *consoleTab) execute(stmts []sqlx.Statement, limit int, more bool) tea.Cmd {
	filter, note := t.runFilter, t.runNote
	t.runFilter, t.runNote = nil, ""
	t.attempt = &runAttempt{stmts: stmts, limit: limit, more: more, text: t.runText, filter: filter, note: note}
	began, total := false, len(stmts)
	if t.manualTx && !t.inTx && sqlx.TransactionEffect(stmts[0].Text, t.dialect) == sqlx.TxNone {
		stmts = append([]sqlx.Statement{{Text: sqlx.BeginSQL(t.dialect), Start: -1, End: -1}}, stmts...)
		began = true
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.gen++
	t.running, t.cancel, t.started = true, cancel, time.Now()
	t.setStatus("", false)
	cfg, _ := t.backend.Connection(t.conn)
	ro := cfg.IsReadOnly()
	gen, id, b, conn, ns, d := t.gen, t.id, t.backend, t.conn, t.ns, t.dialect
	text := t.runText
	s, sessRO, inTx := t.session, t.sessionRO, t.inTx
	t.session = nil // owned by the command until runDoneMsg hands it back
	run := func() tea.Msg {
		defer cancel()
		msg := runDoneMsg{tabID: id, conn: conn, gen: gen, more: more, limit: limit, readOnly: ro, began: began, text: text, filter: filter, note: note, total: total}
		if s != nil && sessRO != ro {
			// :set [no]readonly changed the connection: the session's
			// server-side read-only setting no longer matches.
			_ = s.Close()
			s = nil
			msg.reopened = true
		}
		sess, reconnected, err := ensureSession(ctx, b, conn, ns, s, inTx)
		msg.reconnected = reconnected
		if err != nil {
			msg.err = err
			return msg
		}
		msg.results = runner.Run(ctx, sess, stmts, d, limit)
		if n := len(msg.results); n > 0 && db.IsConnLost(msg.results[n-1].Err) {
			_ = sess.Close()
			sess = nil
		}
		msg.session = sess
		return msg
	}
	return tea.Batch(run, t.tick(gen))
}

func (t *consoleTab) tick(gen int) tea.Cmd {
	id := t.id
	return tea.Tick(t.timings.tick, func(time.Time) tea.Msg { return runTickMsg{tabID: id, gen: gen} })
}

func (t *consoleTab) cancelRun() tea.Cmd {
	if !t.running {
		t.setStatus("no query is running · :qa quits", false)
		return nil
	}
	t.cancel()
	t.setStatus("canceling…", false)
	return nil
}

func (t *consoleTab) loadMore() tea.Cmd {
	if t.res.changes() > 0 {
		t.setStatus(t.res.unsavedNote(), true)
		return nil
	}
	switch {
	case t.running:
		t.setStatus("a query is already running", true)
	case t.last == nil || !t.last.Truncated:
		t.setStatus("all rows are loaded", false)
	case sqlx.Analyze(t.last.SQL, t.dialect).Kind != sqlx.Read:
		t.setStatus("only read statements are re-run to load more rows", true)
	default:
		t.runText = ""
		f := t.shown
		t.runFilter = &f
		return t.execute([]sqlx.Statement{{Text: t.last.SQL}}, t.lastLimit+t.maxRows, true)
	}
	return nil
}

func (t *consoleTab) Update(msg tea.Msg) tea.Cmd {
	if cmd, ok := t.res.update(msg); ok {
		return cmd
	}
	switch m := msg.(type) {
	case consoleMetaMsg:
		t.metaLoaded(m)
		return nil
	case fileSavedMsg:
		return t.saved(m)
	case saveConfirmedMsg:
		return t.save(m.path, true, m.quit)
	case consoleEditsMsg:
		if m.gen == t.gen {
			return t.editsDone(m)
		}
		return nil
	case runTickMsg:
		if t.running && m.gen == t.gen {
			return t.tick(m.gen)
		}
	case confirmRunMsg:
		if m.gen == t.gen && t.pending != nil {
			stmts := t.pending
			t.pending = nil
			return t.execute(stmts, t.maxRows, false)
		}
	case runDoneMsg:
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
		id, path, editErr := t.id, m.path, m.err
		return func() tea.Msg {
			defer func() { _ = os.Remove(path) }()
			if editErr != nil {
				return editTextMsg{tabID: id, err: editErr}
			}
			data, err := os.ReadFile(path)
			return editTextMsg{tabID: id, text: string(data), err: err}
		}
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

func (t *consoleTab) done(m runDoneMsg) tea.Cmd {
	t.running, t.cancel = false, nil
	t.session, t.sessionRO = m.session, m.readOnly
	t.sessionNotes(m.reconnected, m.reopened, m.readOnly)
	if errors.Is(m.err, errTxLost) {
		t.clearTx()
	}
	t.trackTx(m.results, m.session == nil && m.err == nil)
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
	var shown *runner.Result
	for i := range m.results {
		r := &m.results[i]
		t.log(describe(r))
		if r.Err == nil && r.Rows != nil {
			shown = r
		}
	}
	if len(m.results) == 0 {
		t.setStatus("canceled", true)
		return nil
	}
	last := m.results[len(m.results)-1]
	var meta tea.Cmd
	if shown != nil {
		t.shown = resultFilter{base: shown.SQL}
		if m.filter != nil {
			t.shown = *m.filter
		}
		t.res.failedWhere = ""
		meta = t.resetResult(shown.Rows)
		if m.more {
			t.grid.ReplaceResult(t.res.table.Result())
		} else {
			t.grid.SetResult(t.res.table.Result())
		}
		t.last, t.lastLimit = shown, m.limit
	} else if m.filter != nil && last.Err != nil {
		t.res.failedWhere = m.filter.where
	}
	switch {
	case last.Err != nil:
		status := t.locateError(m.text, last)
		n := len(m.results)
		if m.began {
			n--
		}
		if m.total > 1 && n > 0 {
			status = fmt.Sprintf("statement %d/%d failed: %s", n, m.total, status)
		}
		t.setStatus(status, true)
		// A failed re-read of the shown result (filter, load more) keeps
		// its rows on screen.
		t.showMessages = t.showMessages || m.filter == nil
	case shown == nil || len(shown.Rows.Columns) == 0:
		t.setStatus(fmt.Sprintf("%d statement(s) ok", len(m.results)), false)
		t.showMessages = true
	default:
		t.showMessages = false
		if m.note != "" {
			t.setStatus(m.note, false)
		}
	}
	recorded := m.results
	if m.began && len(recorded) > 0 {
		recorded = recorded[1:] // the injected BEGIN is not history
	}
	return tea.Batch(t.record(recorded), t.staleCatalog(m.results), meta)
}

// sessionNotes logs that the session was reconnected or reopened.
func (t *consoleTab) sessionNotes(reconnected, reopened, readOnly bool) {
	if reconnected {
		t.log("reconnected to " + t.conn)
	}
	if reopened {
		mode := "read-write"
		if readOnly {
			mode = "read-only"
		}
		t.log("session reopened " + mode + " (session state such as temporary tables was reset)")
	}
}

// staleCatalog tells the root model that DDL ran, so the catalog changed.
func (t *consoleTab) staleCatalog(results []runner.Result) tea.Cmd {
	for _, r := range results {
		if r.Err == nil && sqlx.Analyze(r.SQL, t.dialect).Kind == sqlx.DDL {
			t.metas = nil // the tables behind results may have changed
			conn, database := t.conn, t.ns.Database
			return func() tea.Msg { return metaStaleMsg{conn: conn, database: database} }
		}
	}
	return nil
}

func (t *consoleTab) record(results []runner.Result) tea.Cmd {
	if len(results) == 0 {
		return nil
	}
	st, now := t.store, time.Now()
	entries := make([]history.Entry, len(results))
	for i, r := range results {
		entries[i] = history.Entry{Time: now, Conn: t.conn, Database: t.ns.Database, SQL: r.SQL, Duration: r.Duration}
		if r.Err != nil {
			entries[i].Err = errorText(r.Err)
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

func describe(r *runner.Result) string {
	took := r.Duration.Round(time.Millisecond)
	switch {
	case r.Err != nil:
		return fmt.Sprintf("✗ %s — %s", oneLine(r.SQL), errorText(r.Err))
	case r.Rows != nil && len(r.Rows.Columns) > 0:
		s := fmt.Sprintf("✓ %s — %d rows in %s", oneLine(r.SQL), len(r.Rows.Rows), took)
		if r.Truncated {
			s += " (truncated)"
		}
		return s
	case r.Rows != nil:
		return fmt.Sprintf("✓ %s — ok in %s", oneLine(r.SQL), took)
	}
	return fmt.Sprintf("✓ %s — %d rows affected in %s", oneLine(r.SQL), r.Affected, took)
}

func errorText(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case db.IsConnLost(err):
		return "connection lost — the statement may or may not have run; the next run reconnects"
	}
	return err.Error()
}

// oneLine shortens a statement for the message log.
func oneLine(sql string) string {
	s := strings.Join(strings.Fields(sql), " ")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:59]) + "…"
	}
	return s
}

// ensureSession returns a live session: s if it still answers a ping,
// otherwise a newly opened one (reconnected reports that). Statements are
// never retried here. Inside a transaction a dead session is not replaced:
// the transaction is gone, and the caller reports errTxLost.
func ensureSession(ctx context.Context, b Backend, conn string, ns db.Namespace, s db.Session, inTx bool) (db.Session, bool, error) {
	if s != nil {
		pctx, cancel := context.WithTimeout(ctx, pingTimeout)
		err := s.Ping(pctx)
		cancel()
		if err == nil {
			return s, false, nil
		}
		slog.Info("console session lost", "conn", conn, "in_transaction", inTx, "err", err)
		_ = s.Close()
		if inTx {
			return nil, false, errTxLost
		}
	}
	reconnected := s != nil
	ns2, err := openSession(ctx, b, conn, ns)
	if db.IsConnLost(err) {
		b.Reset(conn)
		ns2, err = openSession(ctx, b, conn, ns)
		reconnected = true
	}
	return ns2, reconnected, err
}

func openSession(ctx context.Context, b Backend, conn string, ns db.Namespace) (db.Session, error) {
	octx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()
	c, err := b.Conn(octx, conn, ns.Database)
	if err != nil {
		return nil, err
	}
	s, err := c.Session(octx)
	if err != nil {
		return nil, err
	}
	cfg, _ := b.Connection(conn)
	if q := scopeSQL(cfg.Driver, c.Dialect(), ns); q != "" {
		if _, err := s.Exec(octx, q); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	return s, nil
}

// scopeSQL points a fresh session at the namespace the console was opened on.
func scopeSQL(driver string, d db.Dialect, ns db.Namespace) string {
	switch {
	case driver == "mysql" && ns.Database != "":
		return "USE " + d.QuoteIdent(ns.Database)
	case driver == "postgres" && ns.Schema != "":
		return "SET search_path TO " + d.QuoteIdent(ns.Schema) + ", public"
	}
	return ""
}

func (t *consoleTab) shutdown() func() {
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

type (
	draftLoadedMsg struct {
		tabID int
		text  string
	}
	draftTickMsg struct{ tabID, gen int }
	editFileMsg  struct {
		tabID int
		path  string
		err   error
	}
	editDoneMsg struct {
		tabID int
		path  string
		err   error
	}
	editTextMsg struct {
		tabID int
		text  string
		err   error
	}
	// clipboardTextMsg is the system clipboard read for a "+p.
	clipboardTextMsg struct {
		tabID int
		text  string
	}
	// tabJumpMsg is gt / gT in a console; the root model switches tabs.
	tabJumpMsg vimedit.TabJump
)

func (m draftLoadedMsg) TabID() int   { return m.tabID }
func (m draftTickMsg) TabID() int     { return m.tabID }
func (m editFileMsg) TabID() int      { return m.tabID }
func (m editDoneMsg) TabID() int      { return m.tabID }
func (m editTextMsg) TabID() int      { return m.tabID }
func (m clipboardTextMsg) TabID() int { return m.tabID }

func (t *consoleTab) Init() tea.Cmd {
	if t.scratchKey != "" || t.file != nil {
		return nil
	}
	id, st, key := t.id, t.store, t.Key()
	return func() tea.Msg {
		text, err := st.Draft(key)
		if err != nil {
			slog.Warn("load draft", "err", err)
		}
		return draftLoadedMsg{tabID: id, text: text}
	}
}

func (t *consoleTab) scheduleDraft() tea.Cmd {
	t.draftGen++
	id, gen := t.id, t.draftGen
	return tea.Tick(t.timings.draft, func(time.Time) tea.Msg { return draftTickMsg{tabID: id, gen: gen} })
}

func (t *consoleTab) saveDraft() func() {
	if t.scratchKey != "" || t.file != nil {
		return func() {}
	}
	st, key, text := t.store, t.Key(), t.editor.Value()
	return func() {
		if err := st.SaveDraft(key, text); err != nil {
			slog.Warn("save draft", "err", err)
		}
	}
}

// insertText appends sql to the editor (or fills an empty editor).
func (t *consoleTab) insertText(sql string) tea.Cmd {
	if cur := t.editor.Value(); cur == "" {
		t.editor.ReplaceAll(sql)
	} else {
		t.editor.ReplaceAll(cur + "\n" + sql)
	}
	t.onResults = false
	return t.scheduleDraft()
}

func (t *consoleTab) externalEdit() tea.Cmd {
	id, text := t.id, t.editor.Value()
	return func() tea.Msg {
		f, err := os.CreateTemp("", "sqlvim-*.sql")
		if err != nil {
			return editFileMsg{tabID: id, err: err}
		}
		_, err = f.WriteString(text)
		return editFileMsg{tabID: id, path: f.Name(), err: errors.Join(err, f.Close())}
	}
}

// ---- view ----

func (t *consoleTab) View(focused bool) string { return t.Framed("", focused, false) }

// Framed draws the editor and the results in frames of their own, the
// focused one's border lit, under title (the tab bar).
func (t *consoleTab) Framed(title string, focused, danger bool) string {
	if t.height == 0 || t.width == 0 {
		return ""
	}
	onEditor, onResults := focused && !t.onResults, focused && t.onResults
	resultsH := max(t.height-3, 0) - t.editorH
	results := ""
	if t.showMessages {
		results = t.messagesView(resultsH)
	} else {
		results = textutil.PadLines(t.grid.View(onResults), resultsH)
	}
	t.editor.SetFocused(onEditor)
	w := t.width + 2
	top := theme.Frame(title, t.editor.View(), w, t.editorH+2, onEditor, danger)
	bottom := theme.Frame(t.resultsTitle(), results+"\n"+t.statusLine(), w, resultsH+3, onResults, danger)
	return t.drawPopup(top + "\n" + bottom)
}

func (t *consoleTab) resultsTitle() string {
	if t.showMessages {
		return "Messages"
	}
	label := "Results"
	if rs := t.grid.Result(); rs != nil {
		label += fmt.Sprintf(" · %d rows", len(rs.Rows))
		if t.shown.where != "" {
			label += " · where " + oneLine(t.shown.where)
		}
		if t.last != nil && t.last.Truncated {
			label += " · truncated · ] more"
		}
	}
	return label
}

func (t *consoleTab) messagesView(h int) string {
	start := max(len(t.messages)-h, 0)
	lines := make([]string, 0, h)
	for _, m := range t.messages[start:] {
		lines = append(lines, textutil.Fit(textutil.Sanitize(m), t.width))
	}
	return textutil.PadLines(strings.Join(lines, "\n"), h)
}

func (t *consoleTab) statusLine() string {
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
	if n := t.res.changes(); n > 0 {
		return theme.Accent.Render("● "+plural(n, "change")) +
			theme.Muted.Render(textutil.Fit(" · "+t.res.unsavedHint("submit", "discard"), t.width))
	}
	hint := joinHints(
		keyHint(t.keys, keymap.Console, keymap.RunStatement, "run"),
		keyHint(t.keys, keymap.Console, keymap.RunAll, "run all"),
		keyHint(t.keys, keymap.Console, keymap.FocusResults, "results"),
		keyHint(t.keys, keymap.Console, keymap.ExternalEdit, "$EDITOR"),
		keyHint(t.keys, keymap.Console, keymap.RunFromCursor, "run from here"),
		"i insert · v select · u undo", // vim basics last: cut first when narrow
	)
	if t.onResults {
		hint = joinHints(
			keyHint(t.keys, keymap.Results, keymap.EditCell, "edit"),
			keyHint(t.keys, keymap.Results, keymap.FilterPrompt, "filter"),
			keyHint(t.keys, keymap.Results, keymap.ToggleMessages, "messages"),
			keyHint(t.keys, keymap.Results, keymap.FocusEditor, "editor"),
			"hjkl move · yy copy row · K show cell · / search") // grid basics last: cut first when narrow
	} else if t.editor.Visual() {
		hint = "enter runs the selection · y d c > < · esc cancel"
	}
	if t.manualTx {
		hint = "manual commit · :commit / :rollback · " + hint
	}
	return theme.Muted.Render(textutil.Fit(hint, t.width))
}

// locateError moves the editor cursor to where the server says r failed, or
// to the start of r when it does not say, when the editor still holds the
// text that ran, and returns the status line for the error.
func (t *consoleTab) locateError(text string, r runner.Result) string {
	msg := errorText(r.Err)
	if r.Start < 0 || text == "" || text != t.editor.Value() {
		return msg
	}
	t.onResults = false
	off, ok := db.ErrorOffset(r.Err)
	if !ok {
		t.editor.SetCursorOffset(r.Start)
		return msg
	}
	pos := min(r.Start+off, len(text))
	t.editor.SetCursorOffset(pos)
	before := text[:pos]
	line := strings.Count(before, "\n") + 1
	col := utf8.RuneCountInString(before[strings.LastIndexByte(before, '\n')+1:]) + 1
	return fmt.Sprintf("line %d, col %d: %s", line, col, msg)
}

// runAttempt is what execute was asked to run.
type runAttempt struct {
	stmts  []sqlx.Statement
	limit  int
	more   bool
	text   string
	filter *resultFilter
	note   string
}

// retryAfterPassword runs the latest attempt again; the root model calls it
// once the password whose absence made it fail is entered.
func (t *consoleTab) retryAfterPassword() tea.Cmd {
	if t.res.retrySubmit {
		t.res.retrySubmit = false
		return t.res.submit()
	}
	a := t.attempt
	if a == nil || t.running {
		return nil
	}
	t.runText, t.runFilter, t.runNote = a.text, a.filter, a.note
	return t.execute(a.stmts, a.limit, a.more)
}
