package ui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/export"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/meta"
	"github.com/shepherd-yun/sqlvim/internal/session"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

const defaultWhichKeyDelay = 300 * time.Millisecond

const welcome = `Select a table in the tree and press l or enter.

<space>c   SQL console
<space>s   table structure
<space>n   new connection
?          help
:qa        quit`

type focus int

const (
	focusTree focus = iota
	focusTabs
)

type whichKeyMsg struct{ gen int }

// quitConfirmedMsg and closeTabConfirmedMsg come from the open-transaction
// confirmations.
type (
	quitConfirmedMsg     struct{}
	closeTabConfirmedMsg struct{ tabID int }
)

// Model is the root Bubble Tea model.
type Model struct {
	backend  Backend
	store    Store
	catalogs *meta.Cache
	home     string
	settings config.Settings
	keys     *keymap.Keymap

	width, height int
	focus         focus
	showTree      bool
	tree          *tree.Model

	tabs      []tab
	active    int
	nextTabID int

	pending       []string
	pendingGen    int
	showWhichKey  bool
	whichKeyDelay time.Duration

	overlay   overlay.Overlay
	cmdline   *overlay.CmdLine
	retryNode *tree.Node

	status    string
	statusErr bool

	timings    timings
	lastSearch string
}

// New builds the root model with one tree root per configured connection.
func New(b Backend, st Store, settings config.Settings) *Model {
	home, _ := os.UserHomeDir()
	conns := b.Connections()
	roots := make([]*tree.Node, len(conns))
	for i, c := range conns {
		roots[i] = &tree.Node{Kind: tree.KindConn, Name: c.Name, Conn: c.Name, Prod: c.Env == config.EnvProd}
	}
	return &Model{
		backend:       b,
		store:         st,
		catalogs:      meta.NewCache(),
		home:          home,
		settings:      settings,
		keys:          keymap.Default(),
		showTree:      true,
		tree:          tree.New(roots),
		whichKeyDelay: defaultWhichKeyDelay,
		timings:       defaultTimings,
	}
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if f, ok := msg.(connFailure); ok {
		conn, err := f.failure()
		defer m.noteConn(conn, err)
		if o := sshPrompt(err); o != nil && m.overlay == nil {
			m.overlay = o
		}
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
	case tea.KeyPressMsg:
		return m, m.handleKey(msg)
	case whichKeyMsg:
		if msg.gen == m.pendingGen && len(m.pending) > 0 {
			m.showWhichKey = true
		}
	case childrenLoadedMsg:
		return m, m.childrenLoaded(msg)
	case metaLoadedMsg:
		return m, m.metaLoaded(msg)
	case metaStaleMsg:
		return m, m.metaStale(msg)
	case overlay.PasswordSubmittedMsg:
		return m, m.passwordSubmitted(msg)
	case passphraseMsg:
		return m, m.passphraseEntered(msg)
	case hostTrustedMsg:
		return m, m.hostTrusted(msg)
	case overlay.CommandMsg:
		return m, m.runCommand(msg.Text)
	case quitConfirmedMsg:
		return m, m.quit()
	case closeTabConfirmedMsg:
		for i, t := range m.tabs {
			if t.ID() == msg.tabID {
				return m, m.closeTab(i)
			}
		}
	case openOverlayMsg:
		m.overlay = msg.overlay
	case tea.PasteMsg:
		// Like keys, a paste goes to the command line, then a popup, then
		// a console in insert mode.
		switch p, ok := m.overlay.(overlay.Paster); {
		case m.cmdline != nil:
			m.cmdline.Paste(msg.Content)
		case ok:
			p.Paste(msg.Content)
		case m.overlay != nil:
		default:
			if c := m.capturingTab(); c != nil {
				return m, c.Input(msg)
			}
		}
	case historyLoadedMsg:
		m.historyLoaded(msg)
	case historyPickedMsg:
		return m, m.useHistory(msg.entry)
	case exportScopeMsg:
		m.exportScope(msg)
	case exportPathMsg:
		return m, m.exportTo(msg)
	case exportDoneMsg:
		m.exportDone(msg)
	case overlay.ConnFormMsg:
		if msg.Original != "" {
			return m, updateConnection(m.backend, msg)
		}
		return m, addConnection(m.backend, msg)
	case connAddedMsg:
		m.connAdded(msg)
	case connUpdatedMsg:
		return m, m.connUpdated(msg)
	case deleteConnMsg:
		return m, removeConnection(m.backend, msg.name)
	case connRemovedMsg:
		return m, m.connRemoved(msg)
	case overlay.SearchMsg:
		m.lastSearch = msg.Text
		m.find(true)
	case tabMsg:
		if t := m.tabByID(msg.TabID()); t != nil {
			return m, t.Update(msg)
		}
		if d, ok := msg.(runDoneMsg); ok && d.session != nil {
			s := d.session
			return m, func() tea.Msg { _ = s.Close(); return nil }
		}
	}
	return m, nil
}

// ---- keys ----

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	m.status, m.statusErr = "", false
	if m.cmdline != nil {
		next, cmd := m.cmdline.HandleKey(msg)
		if next == nil {
			m.cmdline = nil
		}
		return cmd
	}
	if m.overlay != nil {
		next, cmd := m.overlay.HandleKey(msg)
		m.overlay = next
		return cmd
	}
	if c := m.capturingTab(); c != nil {
		return c.Input(msg)
	}
	key := msg.String()
	if key == "esc" && len(m.pending) > 0 {
		m.clearPending()
		return nil
	}
	m.pending = append(m.pending, key)
	action, res := m.keys.Resolve(m.context(), m.pending)
	switch res {
	case keymap.Pending:
		m.pendingGen++
		gen := m.pendingGen
		return tea.Tick(m.whichKeyDelay, func(time.Time) tea.Msg { return whichKeyMsg{gen: gen} })
	case keymap.Matched:
		m.clearPending()
		return m.dispatch(action)
	default:
		m.clearPending()
		return nil
	}
}

func (m *Model) clearPending() {
	m.pending = nil
	m.showWhichKey = false
}

func (m *Model) context() keymap.Context {
	if m.focus == focusTabs {
		if t := m.activeTab(); t != nil {
			return t.Context()
		}
	}
	return keymap.Tree
}

func (m *Model) dispatch(a keymap.Action) tea.Cmd {
	if n, ok := keymap.TabNumber(a); ok {
		m.gotoTab(n - 1)
		return nil
	}
	if f, ok := keymap.CopyFormat(a); ok {
		return m.copyAs(export.Format(f))
	}
	if f, ok := keymap.ExportFormat(a); ok {
		return m.exportAs(export.Format(f))
	}
	switch a {
	case keymap.Quit:
		return m.requestQuit(false)
	case keymap.ToggleTx:
		if c, ok := m.activeTab().(*consoleTab); ok {
			return c.toggleTx()
		}
		m.setStatus("manual commit belongs to a console · <space>c opens one", true)
	case keymap.Cancel:
		if c, ok := m.activeTab().(canceler); ok && m.focus == focusTabs {
			return c.cancelRun()
		}
		m.setStatus("no query is running · :qa quits", false)
	case keymap.OpenConsole:
		conn, ns := m.contextNamespace()
		if conn == "" {
			m.setStatus("select a connection first", true)
			return nil
		}
		return m.openConsole(conn, ns)
	case keymap.History:
		return loadHistory(m.store)
	case keymap.Search:
		m.cmdline = overlay.NewSearchLine()
	case keymap.SearchNext:
		m.find(true)
	case keymap.SearchPrev:
		m.find(false)
	case keymap.CommandLine:
		m.cmdline = overlay.NewCmdLine()
	case keymap.Help:
		m.overlay = overlay.NewHelp(m.keys.Help(m.context()))
	case keymap.FocusNext, keymap.FocusPrev:
		if m.focus == focusTree {
			m.setFocus(focusTabs)
		} else {
			m.setFocus(focusTree)
		}
	case keymap.FocusLeft:
		m.setFocus(focusTree)
	case keymap.FocusRight:
		m.setFocus(focusTabs)
	case keymap.ToggleTree:
		m.toggleTree()
	case keymap.YankCell, keymap.YankRow:
		return m.yank(a)
	case keymap.NewConnection:
		m.overlay = overlay.NewConnForm()
	case keymap.Refresh:
		var cmd tea.Cmd
		if conn, ns := m.contextNamespace(); conn != "" {
			cmd = m.reloadMeta(conn, ns.Database)
		}
		if m.focus == focusTree {
			return tea.Batch(cmd, m.treeAction(a))
		}
		if t := m.activeTab(); t != nil {
			return tea.Batch(cmd, t.Handle(a))
		}
		return cmd
	case keymap.OpenStructure:
		return m.openStructureFromContext()
	default:
		if m.focus == focusTree {
			return m.treeAction(a)
		}
		if t := m.activeTab(); t != nil {
			return t.Handle(a)
		}
	}
	return nil
}

func (m *Model) treeAction(a keymap.Action) tea.Cmd {
	t := m.tree
	switch a {
	case keymap.Down:
		t.Move(1)
	case keymap.Up:
		t.Move(-1)
	case keymap.Top:
		t.Top()
	case keymap.Bottom:
		t.Bottom()
	case keymap.HalfPageDown:
		t.HalfPage(1)
	case keymap.HalfPageUp:
		t.HalfPage(-1)
	case keymap.Collapse:
		t.Collapse()
	case keymap.Expand:
		n := t.Selected()
		if n == nil {
			return nil
		}
		if n.Kind == tree.KindConn && n.Broken {
			m.backend.Reset(n.Conn)
			m.tree.ResetConn(n.Conn)
		}
		if n.IsTable() {
			return m.openData(n.Conn, n.TableRef())
		}
		if t.Expand() {
			return loadChildren(m.backend, n)
		}
	case keymap.Refresh:
		if n := t.Selected(); n != nil && n.Expandable() && !n.Loading {
			t.StartLoad(n)
			return loadChildren(m.backend, n)
		}
	case keymap.Password:
		if n := t.Selected(); n != nil {
			m.retryNode = nil
			m.overlay = overlay.NewPassword(n.Conn)
		}
	case keymap.EditConnection:
		if n := t.Selected(); n != nil && m.canChange(n.Conn) {
			if cfg, ok := m.backend.Connection(n.Conn); ok {
				m.overlay = overlay.NewEditConnForm(cfg)
			}
		}
	case keymap.DeleteConnection:
		if n := t.Selected(); n != nil && m.canChange(n.Conn) {
			m.overlay = overlay.NewConfirm("Delete connection", []string{
				"Delete " + n.Conn + " from connections.toml and its saved password?",
				"Its open tabs are closed. Type the connection name to confirm.",
			}, n.Conn, deleteConnMsg{name: n.Conn})
		}
	}
	return nil
}

// canChange reports whether conn may be edited or deleted: not while a
// console on it holds an open transaction.
func (m *Model) canChange(conn string) bool {
	for _, t := range m.tabs {
		if c, ok := t.(*consoleTab); ok && c.conn == conn && c.openTx() {
			m.setStatus("a console on "+conn+" has an open transaction · :commit or :rollback first", true)
			return false
		}
	}
	return true
}

func (m *Model) childrenLoaded(msg childrenLoadedMsg) tea.Cmd {
	if errors.Is(msg.err, session.ErrPasswordRequired) {
		m.tree.CancelLoad(msg.node)
		m.retryNode = msg.node
		m.overlay = overlay.NewPassword(msg.node.Conn)
		return nil
	}
	if sshPrompt(msg.err) != nil {
		// The popup opened in Update; load again once it is answered.
		m.tree.CancelLoad(msg.node)
		m.retryNode = msg.node
		return nil
	}
	m.tree.SetChildren(msg.node, msg.children, msg.err)
	if msg.err != nil {
		m.setStatus(msg.err.Error(), true)
		return nil
	}
	return m.ensureMeta(msg.node.Conn, msg.node.NS.Database)
}

func (m *Model) passwordSubmitted(msg overlay.PasswordSubmittedMsg) tea.Cmd {
	if err := m.backend.SetPassword(msg.Conn, msg.Password, msg.Save); err != nil {
		m.setStatus("password kept for this session only: "+err.Error(), true)
	}
	node := m.retryNode
	m.retryNode = nil
	if node == nil || node.Conn != msg.Conn {
		m.tree.ResetConn(msg.Conn)
		return nil
	}
	m.tree.StartLoad(node)
	return loadChildren(m.backend, node)
}

func (m *Model) runCommand(text string) tea.Cmd {
	cmd := strings.TrimSpace(text)
	if opt, ok := strings.CutPrefix(cmd, "set "); ok {
		return m.setOption(strings.TrimSpace(opt))
	}
	switch cmd {
	case "":
	case "q", "close":
		return m.requestClose(false)
	case "q!":
		return m.requestClose(true)
	case "qa", "quit":
		return m.requestQuit(false)
	case "qa!":
		return m.requestQuit(true)
	case "commit", "rollback":
		if c, ok := m.activeTab().(*consoleTab); ok {
			return c.endTx(cmd == "commit")
		}
		m.setStatus("no open transaction: the active tab is not a console", true)
	default:
		m.setStatus("unknown command: "+cmd, true)
	}
	return nil
}

// requestClose closes the active tab, asking first when it holds an open
// transaction (unless force). With no tabs it quits, like vim.
func (m *Model) requestClose(force bool) tea.Cmd {
	t := m.activeTab()
	if t == nil {
		return m.requestQuit(force)
	}
	if c, ok := t.(*consoleTab); ok && c.openTx() && !force {
		m.overlay = overlay.NewConfirm("Open transaction", []string{
			c.Title() + " has an open transaction (" + c.TxStatus() + ").",
			"Closing the console rolls it back.",
		}, "", closeTabConfirmedMsg{tabID: t.ID()})
		return nil
	}
	return m.closeTab(m.active)
}

// requestQuit quits, asking first when consoles hold open transactions
// (unless force).
func (m *Model) requestQuit(force bool) tea.Cmd {
	if open := m.openTransactions(); len(open) > 0 && !force {
		m.overlay = overlay.NewConfirm("Open transactions", []string{
			"Uncommitted transactions in: " + strings.Join(open, ", ") + ".",
			"Quitting rolls them back.",
		}, "", quitConfirmedMsg{})
		return nil
	}
	return m.quit()
}

// openTransactions names the consoles with an uncommitted transaction.
func (m *Model) openTransactions() []string {
	var out []string
	for _, t := range m.tabs {
		if c, ok := t.(*consoleTab); ok && c.openTx() {
			out = append(out, c.Title())
		}
	}
	return out
}

// setOption handles :set readonly / noreadonly for the connection in context.
func (m *Model) setOption(opt string) tea.Cmd {
	var ro bool
	switch opt {
	case "readonly", "ro":
		ro = true
	case "noreadonly", "noro":
	default:
		m.setStatus("unknown option: "+opt+" (readonly, noreadonly)", true)
		return nil
	}
	cfg, ok := m.contextConfig()
	if !ok {
		m.setStatus("select a connection first", true)
		return nil
	}
	if cfg.IsReadOnly() == ro {
		state := "read-write"
		if ro {
			state = "read-only"
		}
		m.setStatus(cfg.Name+" is already "+state, false)
		return nil
	}
	for _, t := range m.tabs {
		if c, ok := t.(*consoleTab); ok && c.conn == cfg.Name && c.openTx() {
			m.setStatus("a console on "+cfg.Name+" has an open transaction · :commit or :rollback first", true)
			return nil
		}
	}
	if err := m.backend.SetReadOnly(cfg.Name, ro); err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	switch {
	case ro:
		m.setStatus(cfg.Name+" is read-only again", false)
	case cfg.Env == config.EnvProd:
		m.setStatus(cfg.Name+" is read-write for this session · writes still ask for confirmation", false)
	default:
		m.setStatus(cfg.Name+" is read-write for this session", false)
	}
	return nil
}

func (m *Model) setStatus(s string, isErr bool) { m.status, m.statusErr = s, isErr }

// ---- focus and tabs ----

func (m *Model) setFocus(f focus) {
	switch f {
	case focusTree:
		if !m.showTree {
			m.showTree = true
			m.layout()
		}
		m.focus = focusTree
	case focusTabs:
		if len(m.tabs) > 0 {
			m.focus = focusTabs
		}
	}
}

func (m *Model) toggleTree() {
	if !m.showTree {
		m.showTree = true
	} else if len(m.tabs) > 0 {
		m.showTree = false
		m.focus = focusTabs
	}
	m.layout()
}

func (m *Model) activeTab() tab {
	if m.active < 0 || m.active >= len(m.tabs) {
		return nil
	}
	return m.tabs[m.active]
}

func (m *Model) tabByID(id int) tab {
	for _, t := range m.tabs {
		if t.ID() == id {
			return t
		}
	}
	return nil
}

func (m *Model) gotoTab(i int) {
	if i >= 0 && i < len(m.tabs) {
		m.active = i
		m.focus = focusTabs
	}
}

func (m *Model) closeTab(i int) tea.Cmd {
	cmd := shutdownCmd(m.tabs[i])
	m.tabs = append(m.tabs[:i], m.tabs[i+1:]...)
	m.active = textutil.Clamp(m.active, 0, len(m.tabs)-1)
	if len(m.tabs) == 0 {
		m.showTree = true
		m.focus = focusTree
	}
	m.layout()
	return cmd
}

func (m *Model) openTab(key string, create func(id int) tab) tea.Cmd {
	for i, t := range m.tabs {
		if t.Key() == key {
			m.gotoTab(i)
			return nil
		}
	}
	m.nextTabID++
	t := create(m.nextTabID)
	m.tabs = append(m.tabs, t)
	m.active = len(m.tabs) - 1
	m.focus = focusTabs
	m.layout()
	return t.Init()
}

func (m *Model) openData(conn string, ref db.TableRef) tea.Cmd {
	return tea.Batch(m.openTab(dataTabKey(conn, ref), func(id int) tab {
		return newDataTab(id, m.backend, conn, ref, m.settings)
	}), m.ensureMeta(conn, ref.Database))
}

func (m *Model) openStructure(conn string, ref db.TableRef) tea.Cmd {
	return tea.Batch(m.openTab(structureTabKey(conn, ref), func(id int) tab {
		return newStructureTab(id, m.backend, conn, ref)
	}), m.ensureMeta(conn, ref.Database))
}

func (m *Model) openStructureFromContext() tea.Cmd {
	if m.focus == focusTree {
		if n := m.tree.Selected(); n != nil && n.IsTable() {
			return m.openStructure(n.Conn, n.TableRef())
		}
	} else if t := m.activeTab(); t != nil {
		if conn, ref := t.Target(); ref.Name != "" {
			return m.openStructure(conn, ref)
		}
	}
	m.setStatus("select a table first", true)
	return nil
}

// capturingTab returns the focused tab when it wants raw input (INSERT mode).
func (m *Model) capturingTab() inputCapturer {
	if m.focus != focusTabs {
		return nil
	}
	if c, ok := m.activeTab().(inputCapturer); ok && c.CapturesInput() {
		return c
	}
	return nil
}

func (m *Model) mode() string {
	if m.focus == focusTabs {
		if md, ok := m.activeTab().(moder); ok {
			return md.Mode()
		}
	}
	return "NORMAL"
}

func (m *Model) find(forward bool) {
	q := m.lastSearch
	if q == "" {
		m.setStatus("no previous search", true)
		return
	}
	found := false
	if m.focus == focusTree {
		found = m.tree.Find(q, forward)
	} else if f, ok := m.activeTab().(finder); ok {
		found = f.Find(q, forward)
	}
	if !found {
		m.setStatus("pattern not found: "+q, true)
	}
}

// contextNamespace is the connection and namespace the user is looking at.
func (m *Model) contextNamespace() (string, db.Namespace) {
	if m.focus == focusTabs {
		if t := m.activeTab(); t != nil {
			conn, ref := t.Target()
			return conn, ref.Namespace
		}
	}
	if n := m.tree.Selected(); n != nil {
		return n.Conn, n.NS
	}
	return "", db.Namespace{}
}

func (m *Model) openConsole(conn string, ns db.Namespace) tea.Cmd {
	return tea.Batch(m.openTab(consoleTabKey(conn, ns), func(id int) tab {
		return newConsoleTab(id, m.backend, m.store, m.catalogs, conn, ns, m.settings, m.timings)
	}), m.ensureMeta(conn, ns.Database))
}

func shutdownCmd(t tab) tea.Cmd {
	s, ok := t.(shutdowner)
	if !ok {
		return nil
	}
	f := s.shutdown()
	return func() tea.Msg { f(); return nil }
}

// quit runs every tab's cleanup, then quits.
func (m *Model) quit() tea.Cmd {
	var fs []func()
	for _, t := range m.tabs {
		if s, ok := t.(shutdowner); ok {
			fs = append(fs, s.shutdown())
		}
	}
	return func() tea.Msg {
		for _, f := range fs {
			f()
		}
		return tea.QuitMsg{}
	}
}

const historyLimit = 500

type historyLoadedMsg struct {
	entries []history.Entry
	err     error
}

type historyPickedMsg struct{ entry history.Entry }

func loadHistory(st Store) tea.Cmd {
	return func() tea.Msg {
		entries, err := st.History("", historyLimit)
		return historyLoadedMsg{entries: entries, err: err}
	}
}

func historyLabel(e history.Entry) string {
	loc := e.Conn
	if e.Database != "" {
		loc += "/" + e.Database
	}
	mark := "✓"
	if e.Err != "" {
		mark = "✗"
	}
	return fmt.Sprintf("%s %s  %s  %s", mark, e.Time.Local().Format("01-02 15:04"), loc, strings.Join(strings.Fields(e.SQL), " "))
}

func (m *Model) historyLoaded(msg historyLoadedMsg) {
	switch {
	case msg.err != nil:
		m.setStatus("history: "+msg.err.Error(), true)
	case len(msg.entries) == 0:
		m.setStatus("no SQL history yet", false)
	default:
		labels := make([]string, len(msg.entries))
		for i, e := range msg.entries {
			labels[i] = historyLabel(e)
		}
		entries := msg.entries
		m.overlay = overlay.NewPicker("SQL history", labels, func(i int) tea.Msg { return historyPickedMsg{entry: entries[i]} })
	}
}

func (m *Model) useHistory(e history.Entry) tea.Cmd {
	if c, ok := m.activeTab().(*consoleTab); ok && m.focus == focusTabs {
		return c.insertText(e.SQL)
	}
	if _, ok := m.backend.Connection(e.Conn); !ok {
		m.setStatus("connection "+e.Conn+" no longer exists", true)
		return nil
	}
	open := m.openConsole(e.Conn, db.Namespace{Database: e.Database})
	c := m.activeTab().(*consoleTab)
	return tea.Batch(open, c.insertText(e.SQL))
}

func (m *Model) noteConn(conn string, err error) {
	switch {
	case conn == "":
	case db.IsConnLost(err):
		m.tree.SetBroken(conn, true)
		m.setStatus("connection "+conn+" lost: select it in the tree and press enter to retry", true)
	case err == nil:
		m.tree.SetBroken(conn, false)
	}
}

type connAddedMsg struct {
	conn config.Connection
	err  error
}

func addConnection(b Backend, msg overlay.ConnFormMsg) tea.Cmd {
	return func() tea.Msg {
		err := b.AddConnection(msg.Conn, msg.Password, msg.Save)
		return connAddedMsg{conn: msg.Conn, err: err}
	}
}

func (m *Model) connAdded(msg connAddedMsg) {
	if msg.err != nil {
		m.setStatus("cannot add connection: "+msg.err.Error(), true)
		return
	}
	c := msg.conn
	m.tree.AddRoot(&tree.Node{Kind: tree.KindConn, Name: c.Name, Conn: c.Name, Prod: c.Env == config.EnvProd})
	m.setFocus(focusTree)
	m.setStatus("added connection "+c.Name+" to connections.toml", false)
}

type (
	connUpdatedMsg struct {
		old  string
		conn config.Connection
		err  error
	}
	deleteConnMsg  struct{ name string }
	connRemovedMsg struct {
		name string
		err  error
	}
)

func updateConnection(b Backend, msg overlay.ConnFormMsg) tea.Cmd {
	return func() tea.Msg {
		err := b.UpdateConnection(msg.Original, msg.Conn, msg.Password, msg.Save)
		return connUpdatedMsg{old: msg.Original, conn: msg.Conn, err: err}
	}
}

func removeConnection(b Backend, name string) tea.Cmd {
	return func() tea.Msg { return connRemovedMsg{name: name, err: b.RemoveConnection(name)} }
}

// connUpdated swaps the edited connection into the tree and closes the tabs
// that were opened with its old settings.
func (m *Model) connUpdated(msg connUpdatedMsg) tea.Cmd {
	if msg.err != nil && !errors.Is(msg.err, session.ErrKeychain) {
		m.setStatus("cannot save connection: "+msg.err.Error(), true)
		return nil
	}
	c := msg.conn
	m.catalogs.Forget(msg.old)
	cmd := m.closeTabsOf(msg.old)
	m.tree.ReplaceRoot(msg.old, &tree.Node{Kind: tree.KindConn, Name: c.Name, Conn: c.Name, Prod: c.Env == config.EnvProd})
	m.setFocus(focusTree)
	if msg.err != nil { // saved, but the keychain could not follow
		m.setStatus(msg.err.Error(), true)
	} else {
		m.setStatus("saved connection "+c.Name+" to connections.toml", false)
	}
	return cmd
}

func (m *Model) connRemoved(msg connRemovedMsg) tea.Cmd {
	if msg.err != nil && !errors.Is(msg.err, session.ErrKeychain) {
		m.setStatus("cannot delete connection: "+msg.err.Error(), true)
		return nil
	}
	m.catalogs.Forget(msg.name)
	cmd := m.closeTabsOf(msg.name)
	m.tree.RemoveRoot(msg.name)
	m.setFocus(focusTree)
	if msg.err != nil {
		m.setStatus(msg.err.Error(), true)
	} else {
		m.setStatus("deleted connection "+msg.name, false)
	}
	return cmd
}

// closeTabsOf closes every tab on conn.
func (m *Model) closeTabsOf(conn string) tea.Cmd {
	var cmds []tea.Cmd
	for i := len(m.tabs) - 1; i >= 0; i-- {
		if c, _ := m.tabs[i].Target(); c == conn {
			cmds = append(cmds, m.closeTab(i))
		}
	}
	return tea.Batch(cmds...)
}

// ---- layout and rendering ----

func (m *Model) treeWidth() int {
	if !m.showTree {
		return 0
	}
	return min(textutil.Clamp(m.width/4, 24, 40), m.width)
}

func (m *Model) bodyHeight() int { return max(m.height-1, 0) }

func (m *Model) layout() {
	inner := max(m.bodyHeight()-2, 0)
	m.tree.SetHeight(inner)
	tabsWidth := max(m.width-m.treeWidth()-2, 0)
	for _, t := range m.tabs {
		t.SetSize(tabsWidth, inner)
	}
}

func (m *Model) render() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	bodyH := m.bodyHeight()
	treeW := m.treeWidth()
	var panes []string
	if treeW > 0 {
		focused := m.focus == focusTree
		body := m.tree.View(max(treeW-2, 0), focused)
		if m.tree.Empty() {
			body = theme.Muted.Render("No connections\n<space>n add one")
		}
		panes = append(panes, theme.Box("Connections", body, treeW, bodyH, focused))
	}
	if w := m.width - treeW; w > 0 {
		panes = append(panes, m.renderTabs(w, bodyH))
	}
	screen := lipgloss.JoinHorizontal(lipgloss.Top, panes...) + "\n" + m.renderStatus()
	if m.overlay != nil {
		screen = overlay.Center(screen, m.overlay.View(m.width, m.height), m.width, m.height)
	}
	if m.showWhichKey {
		box := overlay.WhichKey(m.keys.Continuations(m.context(), m.pending))
		x := max(m.width-lipgloss.Width(box), 0)
		y := max(m.height-1-lipgloss.Height(box), 0)
		screen = overlay.PlaceAt(screen, box, x, y)
	}
	return screen
}

func (m *Model) renderTabs(width, height int) string {
	t := m.activeTab()
	if t == nil {
		return theme.Box("sqlvim", welcome, width, height, false)
	}
	focused := m.focus == focusTabs
	conn, _ := t.Target()
	cfg, _ := m.backend.Connection(conn)
	return theme.Frame(m.tabBar(), t.View(focused), width, height, focused, cfg.Env == config.EnvProd)
}

func (m *Model) tabBar() string {
	labels := make([]string, len(m.tabs))
	for i, t := range m.tabs {
		label := fmt.Sprintf("[%d]%s", i+1, textutil.Sanitize(t.Title()))
		if i == m.active {
			labels[i] = theme.ActiveTab.Render(label)
		} else {
			labels[i] = theme.Muted.Render(label)
		}
	}
	return strings.Join(labels, " ")
}

func (m *Model) renderStatus() string {
	if m.cmdline != nil {
		return textutil.Fit(m.cmdline.View(m.width, 1), m.width)
	}
	cfg, ok := m.contextConfig()
	prod := ok && cfg.Env == config.EnvProd
	mode := theme.Mode
	if prod {
		mode = theme.ProdMode
	}
	parts := []string{mode.Render(" " + m.mode() + " ")}
	if prod {
		parts = append(parts, theme.Prod.Render("[PROD]"))
	}
	if ok && cfg.IsReadOnly() {
		parts = append(parts, theme.Muted.Render("RO"))
	}
	if c, ok := m.activeTab().(*consoleTab); ok {
		if tx := c.TxStatus(); tx != "" {
			parts = append(parts, theme.Accent.Render(tx))
		}
	}
	if len(m.pending) > 0 {
		parts = append(parts, m.keys.Display(m.pending))
	}
	if loc := m.location(); loc != "" {
		parts = append(parts, loc)
	}
	if m.status != "" {
		style := theme.Muted
		if m.statusErr {
			style = theme.Error
		}
		parts = append(parts, style.Render(textutil.Sanitize(m.status)))
	}
	left := strings.Join(parts, "  ")
	right := theme.Muted.Render("<space> leader · ? help · : command")
	gap := m.width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return textutil.Fit(left, m.width)
	}
	return left + strings.Repeat(" ", gap) + right
}

// contextConfig is the configuration of the connection the user is looking at.
func (m *Model) contextConfig() (config.Connection, bool) {
	conn, _ := m.contextNamespace()
	if conn == "" {
		return config.Connection{}, false
	}
	return m.backend.Connection(conn)
}

func (m *Model) location() string {
	var conn string
	var ns db.Namespace
	if t := m.activeTab(); m.focus == focusTabs && t != nil {
		var ref db.TableRef
		conn, ref = t.Target()
		ns = ref.Namespace
	} else if n := m.tree.Selected(); n != nil {
		conn, ns = n.Conn, n.NS
	}
	var parts []string
	for _, p := range []string{conn, ns.Database, ns.Schema} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "/")
}
