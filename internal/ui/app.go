package ui

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
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
	"github.com/shepherd-yun/sqlvim/internal/ui/vimedit"
)

const defaultWhichKeyDelay = 300 * time.Millisecond

// welcomeText is the empty tab area, naming the keys the keymap really uses.
func welcomeText(k *keymap.Keymap) string {
	var b strings.Builder
	if keys := k.KeysFor(keymap.Tree, keymap.Open); keys != "" {
		fmt.Fprintf(&b, "Select a table in the tree and press %s to open it.\n\n", keys)
	}
	for _, row := range []struct {
		a     keymap.Action
		label string
	}{
		{keymap.OpenConsole, "console (SQL or Redis)"},
		{keymap.OpenStructure, "table columns and structure"},
		{keymap.NewConnection, "new connection"},
		{keymap.Help, "help"},
	} {
		if keys := k.KeysFor(keymap.Tree, row.a); keys != "" {
			fmt.Fprintf(&b, "%-10s %s\n", keys, row.label)
		}
	}
	b.WriteString(":qa        quit")
	return b.String()
}

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
	backend   Backend
	store     Store
	catalogs  *meta.Cache
	home      string
	settings  config.Settings
	keys      *keymap.Keymap
	workspace string            // shown when not the default workspace
	regs      vimedit.Registers // shared by every vim editor; what y copies goes in too

	width, height int
	focus         focus
	showTree      bool
	full          bool  // + / -: the focused pane fills the screen
	laidFocus     focus // the focus layout last sized the panes for
	tree          *tree.Model
	compareMark   *compareSide // the compare source marked with m

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
	retryTab  int // data or structure tab to reload once a password is entered

	status    string
	statusErr bool

	timings     timings
	favorites   map[favKey]bool // the starred Redis keys
	shownTab    int             // the ID of the tab syncShown last saw on screen; 0: none
	lastSearch  string
	searchConn  string // connection the global search is scoped to
	searchKey   meta.Key
	searchAll   bool                 // the current query searches every connection
	searchHits  []meta.Hit           // the catalog rows the search popup shows
	searchPicks []func(bool) tea.Cmd // opens each row; the flag asks for the structure
	revealing   *revealTarget
	scans       map[redisDBKey]*keyScan // the key scans behind the redis db nodes
	kafkaTrees  map[string]*kafkaTree   // what the kafka connections' trees are built from
	bulk        *bulkDelete             // the running pattern delete, or nil
	bulkGen     int
}

// registerUser is a tab or popup with a vim editor.
type registerUser interface{ UseRegisters(vimedit.Registers) }

// connNode is the tree root of connection c.
func connNode(c config.Connection) *tree.Node {
	return &tree.Node{Kind: tree.KindConn, Name: c.Name, Conn: c.Name, Driver: c.Driver, Prod: c.Env == config.EnvProd}
}

// newTree builds the tree with the icon set the settings ask for.
func newTree(roots []*tree.Node, settings config.Settings) *tree.Model {
	t := tree.New(roots)
	if settings.Icons == config.IconsNerd {
		t.SetIcons(tree.IconsNerd)
	}
	return t
}

// New builds the root model with one tree root per configured connection.
func New(b Backend, st Store, settings config.Settings) *Model {
	home, _ := os.UserHomeDir()
	conns := b.Connections()
	roots := make([]*tree.Node, len(conns))
	for i, c := range conns {
		roots[i] = connNode(c)
	}
	return &Model{
		backend:       b,
		store:         st,
		catalogs:      meta.NewCache(),
		home:          home,
		settings:      settings,
		keys:          keymap.Default(),
		regs:          vimedit.NewRegisters(),
		showTree:      true,
		tree:          newTree(roots, settings),
		whichKeyDelay: defaultWhichKeyDelay,
		timings:       defaultTimings,
		scans:         map[redisDBKey]*keyScan{},
		kafkaTrees:    map[string]*kafkaTree{},
	}
}

// SetKeymap replaces the built-in keymap (keymap.toml). Call it before the
// program starts: open tabs keep the keymap they were created with.
func (m *Model) SetKeymap(k *keymap.Keymap) { m.keys = k }

// SetWorkspace names the workspace sqlvim runs in; the default workspace
// ("" or "default") is not shown.
func (m *Model) SetWorkspace(name string) {
	if name == config.DefaultWorkspace {
		name = ""
	}
	m.workspace = name
}

// title is "sqlvim", followed by the workspace when it is not the default.
func (m *Model) title() string {
	if m.workspace == "" {
		return "sqlvim"
	}
	return "sqlvim — " + m.workspace
}

func (m *Model) Init() tea.Cmd { return loadFavorites(m.store) }

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	if m.workspace != "" {
		v.WindowTitle = m.title()
	}
	return v
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.update(msg)
	m.syncFull()
	if shown := m.syncShown(); shown != nil {
		cmd = tea.Batch(cmd, shown)
	}
	return m, cmd
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if f, ok := msg.(connFailure); ok {
		conn, err := f.failure()
		defer m.noteConn(conn, err)
		if o := sshPrompt(err); o != nil && m.overlay == nil {
			m.overlay = o
		}
		m.askPassword(msg, conn, err)
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
	case redisScanMsg:
		return m, m.scanned(msg)
	case kafkaTreeMsg:
		return m, m.kafkaTreeLoaded(msg)
	case kafkaLagMsg:
		return m, m.kafkaLagged(msg)
	case kafkaFilterMsg:
		m.kafkaFiltered(msg)
	case openGroupMsg:
		return m, m.openGroup(msg.conn, msg.group)
	case redisKeyChangedMsg:
		m.keyChanged(msg)
	case keyOpAskMsg:
		r := msg.req
		switch r.op {
		case keyOpTTL:
			return m, m.askTTL(r.conn, r.db, r.key, r.ttl)
		case keyOpRename:
			return m, m.askRenameKey(r.conn, r.db, r.key, r.typ)
		case keyOpDelete:
			return m, m.askDeleteKey(r.conn, r.db, r.key, r.typ)
		}
	case keyOpInputMsg:
		return m, m.keyOpInput(msg)
	case bulkStepMsg:
		return m, m.bulkStepped(msg)
	case bulkConfirmedMsg:
		return m, m.bulkConfirmed(msg)
	case newKeyTypeMsg:
		m.newKeyType(msg)
	case newKeyNameMsg:
		m.newKeyName(msg)
	case newKeyArgsMsg:
		return m, m.newKeyArgs(msg)
	case keyOpRunMsg:
		return m, m.runKeyOp(msg.req)
	case keyOpMsg:
		return m, m.keyOpDone(msg)
	case redisFilterMsg:
		return m, m.filterKeys(msg.key, msg.text)
	case openKeyMsg:
		return m, checkKey(m.backend, msg)
	case keyCheckedMsg:
		return m, m.keyChecked(msg)
	case metaLoadedMsg:
		return m, m.metaLoaded(msg)
	case metaStaleMsg:
		return m, m.metaStale(msg)
	case overlay.SearchQueryMsg:
		return m, m.runSearch(msg)
	case searchResultsMsg:
		m.searchResults(msg)
	case overlay.SearchPickMsg:
		return m, m.searchPicked(msg)
	case routineLoadedMsg:
		m.routineLoaded(msg)
	case columnsLoadedMsg:
		m.columnsLoaded(msg)
	case resultColumnPickedMsg:
		return m, m.resultColumnPicked(msg)
	case columnPickedMsg:
		return m, m.columnPicked(msg)
	case ddlLoadedMsg:
		return m, m.ddlLoaded(msg)
	case overlay.CopyMsg:
		return m, m.copyText(msg)
	case recentLoadedMsg:
		m.recentLoaded(msg)
	case recentPickedMsg:
		return m, m.openRecent(msg.r)
	case favoritesLoadedMsg:
		m.favoritesLoaded(msg)
	case bigKeysChosenMsg:
		return m, m.bigKeysChosen(msg)
	case bigKeysRunMsg:
		return m, m.runBigKeys(msg)
	case favoritesListedMsg:
		m.favoritesListed(msg)
	case favoritePickedMsg:
		return m, m.openStoredKey(msg.f.Conn, msg.f.Database, msg.f.Name)
	case overlay.PasswordSubmittedMsg:
		return m, m.passwordSubmitted(msg)
	case passphraseMsg:
		return m, m.passphraseEntered(msg)
	case hostTrustedMsg:
		return m, m.hostTrusted(msg)
	case overlay.CommandMsg:
		return m, m.runCommand(msg.Text)
	case fileReadMsg:
		return m, m.fileRead(msg)
	case quitConfirmedMsg:
		return m, m.quit()
	case closeTabConfirmedMsg:
		for i, t := range m.tabs {
			if t.ID() == msg.tabID {
				return m, m.closeTab(i)
			}
		}
	case openOverlayMsg:
		if r, ok := msg.overlay.(registerUser); ok {
			r.UseRegisters(m.regs)
		}
		m.overlay = msg.overlay
	case tabJumpMsg:
		return m, m.jumpTab(vimedit.TabJump(msg))
	case tea.ClipboardMsg:
		// the terminal's answer to an OSC 52 read, asked for by "+p in the active console
		if c, ok := m.activeTab().(pasteTab); ok {
			return m, c.Update(clipboardTextMsg{tabID: c.ID(), text: msg.Content})
		}
	case tea.PasteMsg:
		// Like keys, a paste goes to the command line, then a popup, then
		// a console's editor (in any mode, like Neovim).
		switch p, ok := m.overlay.(overlay.Paster); {
		case m.cmdline != nil:
			m.cmdline.Paste(msg.Content)
		case ok:
			p.Paste(msg.Content)
			if s, isSearch := m.overlay.(*overlay.Search); isSearch {
				return m, m.runSearch(s.Requery())
			}
		case m.overlay != nil:
		case m.focus == focusTabs:
			if c, ok := m.activeTab().(pasteTab); ok {
				return m, c.Paste(msg)
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
	case scriptConsoleMsg:
		return m, m.openScriptConsole(msg.conn, msg.ns, msg.text)
	case compareSourceMsg:
		return m, m.startCompare(msg.src, msg.tgt)
	case menuPickedMsg:
		return m, m.dispatch(msg.action)
	case renameEnteredMsg:
		return m, m.startTableOp(msg.op)
	case tableOpPreparedMsg:
		m.tableOpPrepared(msg)
	case tableOpConfirmedMsg:
		return m, runTableOp(m.backend, m.store, msg.op, msg.sql)
	case tableOpDoneMsg:
		return m, m.tableOpDone(msg)
	case importFileMsg:
		return m, m.openImport(msg)
	case importConfirmedMsg:
		return m, m.importConfirmed(msg)
	case importDoneMsg:
		return m, m.importDone(msg)
	case modifySubmitMsg:
		m.modifySubmit(msg)
	case modifyAppliedMsg:
		return m, m.modifyApplied(msg)

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
		if d, ok := msg.(redisRunMsg); ok && d.session != nil {
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
	if t := m.keyTakingTab(); t != nil && len(m.pending) == 0 {
		if cmd, used := t.TakeKey(msg); used {
			return cmd
		}
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

// connKind is the kind of connection in view, which help and which-key list
// the keys of: the active tab's, or the selected tree node's.
func (m *Model) connKind() keymap.Kind {
	conn := m.contextConn()
	switch {
	case conn == "":
		return keymap.KindAny
	case isRedis(m.backend, conn):
		return keymap.KindRedis
	case isKafka(m.backend, conn):
		return keymap.KindKafka
	}
	return keymap.KindSQL
}

func (m *Model) dispatch(a keymap.Action) tea.Cmd {
	if n, ok := keymap.TabNumber(a); ok {
		if n-1 >= len(m.tabs) {
			return nil
		}
		m.gotoTab(n - 1)
		return m.touchActive()
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
		if _, ok := m.activeTab().(*redisConsoleTab); ok && m.focus == focusTabs {
			m.setStatus(errRedisTx, true)
			return nil
		}
		if m.refuseRedis(m.contextConn()) {
			return nil
		}
		if c, ok := m.activeTab().(*consoleTab); ok {
			return c.toggleTx()
		}
		m.setStatus(joinHints("manual commit belongs to a console", keyHint(m.keys, keymap.Global, keymap.OpenConsole, "opens one")), true)
	case keymap.Cancel:
		if c, ok := m.activeTab().(canceler); ok && m.focus == focusTabs {
			return c.cancelRun()
		}
		if m.focus == focusTree {
			if cmd, ok := m.stopBulk(); ok {
				return cmd
			}
			if m.stopScans() {
				return nil
			}
		}
		m.setStatus("no query is running · :qa quits", false)
	case keymap.OpenConsole:
		conn, ns := m.contextNamespace()
		if conn == "" {
			m.setStatus("select a connection first", true)
			return nil
		}
		if isRedis(m.backend, conn) {
			return m.openRedisConsole(conn, redisDBOf(ns))
		}
		if isKafka(m.backend, conn) {
			m.setStatus("select a topic first", true)
			return nil
		}
		return m.openConsole(conn, ns)
	case keymap.History:
		return loadHistory(m.store)
	case keymap.Search:
		m.cmdline = overlay.NewSearchLine()
	case keymap.SearchNext, keymap.SearchPrev:
		if s, ok := m.activeTab().(filterSearcher); ok && m.focus == focusTabs {
			if cmd, ok := s.searchFilter(a == keymap.SearchNext); ok {
				return cmd
			}
		}
		m.find(a == keymap.SearchNext)
	case keymap.CommandLine:
		m.cmdline = overlay.NewCmdLine()
	case keymap.Help:
		entries := m.keys.Help(m.context(), m.connKind())
		if m.context() == keymap.Console {
			entries = withEditorHelp(entries)
		}
		m.overlay = overlay.NewHelp(entries)
	case keymap.FocusNext, keymap.FocusPrev, keymap.FocusLeft, keymap.FocusRight:
		// Two panes, so moving either way past the edge wraps to the other one.
		if m.focus == focusTree {
			m.setFocus(focusTabs)
		} else {
			m.setFocus(focusTree)
		}
	case keymap.NextTab:
		return m.jumpTab(vimedit.TabJump{By: 1})
	case keymap.PrevTab:
		return m.jumpTab(vimedit.TabJump{By: -1})
	case keymap.ToggleTree:
		m.toggleTree()
	case keymap.FullScreen, keymap.ExitFullScreen:
		m.full = a == keymap.FullScreen
		m.layout()
	case keymap.YankCell, keymap.YankRow:
		return m.yank(a)
	case keymap.GlobalSearch:
		return m.openSearch()
	case keymap.Recent:
		return loadRecent(m.store)
	case keymap.Locate:
		return m.locate()
	case keymap.NewConnection:
		m.overlay = overlay.NewConnForm(m.keys)
	case keymap.OpenKey:
		return m.openKeyPrompt()
	case keymap.Favorites:
		return m.showFavorites()
	case keymap.ToggleFavorite:
		return m.toggleFavoriteHere()
	case keymap.ServerInfo:
		conn := m.contextConn()
		switch {
		case conn == "":
			m.setStatus("select a connection", true)
			return nil
		case isRedis(m.backend, conn):
			return m.openServer(conn)
		case isKafka(m.backend, conn):
			m.setStatus(errKafkaUnsupported.Error()+" yet", true)
			return nil
		}
		return m.openSQLServer(conn)
	case keymap.Refresh:
		var cmd tea.Cmd
		// the server tab refreshes its own section, not the catalog
		_, onServer := m.activeTab().(*sqlServerTab)
		if conn, ns := m.contextNamespace(); conn != "" && (!onServer || m.focus != focusTabs) {
			cmd = m.reloadMeta(conn, ns.Database)
		}
		if m.focus == focusTree {
			return tea.Batch(cmd, m.treeAction(a))
		}
		if t := m.activeTab(); t != nil {
			return tea.Batch(cmd, t.Handle(a))
		}
		return cmd
	case keymap.OpenStructure, keymap.OpenFullStructure, keymap.CopyDDL:
		if m.refuseRedis(m.contextConn()) {
			return nil
		}
		switch a {
		case keymap.OpenStructure:
			return m.openColumnsFromContext()
		case keymap.OpenFullStructure:
			return m.openFullStructure()
		}
		return m.copyDDL()
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
	if n := t.Selected(); n != nil && isKafka(m.backend, n.Conn) {
		if cmd, ok := m.kafkaTreeAction(a, n); ok {
			return cmd
		}
	}
	if sqlOnlyTreeAction(a, t.Selected()) {
		if n := t.Selected(); n != nil && m.refuseRedis(n.Conn) {
			return nil
		}
	}
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
	case keymap.Expand, keymap.FoldOpen:
		return m.expandNode()
	case keymap.Open:
		return m.openNode()
	case keymap.FoldToggle:
		return m.toggleNode()
	case keymap.FoldClose:
		t.Fold()
	case keymap.FoldOpenAll:
		t.OpenLoaded(t.Selected())
	case keymap.FoldCloseAll:
		t.CloseAll(t.Selected())
	case keymap.FoldOpenTree:
		t.OpenTree()
	case keymap.FoldCloseTree:
		t.CloseTree()
	case keymap.FoldFocus:
		t.FocusPath()
	case keymap.Refresh:
		return m.refreshNode()
	case keymap.ImportData:
		return m.importFromNode()
	case keymap.Password:
		if n := t.Selected(); n != nil {
			m.retryNode = nil
			m.overlay = overlay.NewPassword(n.Conn)
		}
	case keymap.EditNode:
		return m.editNode()
	case keymap.MarkCompare:
		m.markCompare()
	case keymap.Compare:
		return m.compare()
	case keymap.NodeActions:
		m.openNodeMenu()
	case keymap.YankName, keymap.YankQualified:
		if n := t.Selected(); n != nil {
			return m.copyNodeName(n, a == keymap.YankQualified)
		}
	case keymap.TruncateTable:
		if n := t.Selected(); n != nil && n.Kind == tree.KindTable {
			return m.startTableOp(tableOp{kind: opTruncate, conn: n.Conn, ref: n.TableRef()})
		}
		m.setStatus("only tables can be truncated", true)
	case keymap.RenameTable:
		if n := t.Selected(); n != nil && n.Kind == tree.KindRedisKey {
			k, _ := redisKeyOf(n)
			return m.askRenameKey(k.conn, k.db, n.Key, n.KeyType)
		}
		if n := t.Selected(); n != nil && n.Table() != nil {
			tbl := n.Table()
			m.askRename(tableOp{kind: opRename, conn: tbl.Conn, ref: tbl.TableRef(), view: tbl.Kind == tree.KindView})
		} else {
			m.setStatus("select a table or view to rename", true)
		}
	case keymap.DeleteNode:
		return m.deleteNode()
	case keymap.NewKey:
		return m.newKeyPrompt()
	case keymap.BigKeys:
		return m.startBigKeys()
	case keymap.FilterPrompt:
		return m.filterKeysPrompt()
	case keymap.FilterClear:
		if n := t.Selected(); n != nil {
			if k, ok := redisKeyOf(n); ok {
				return m.filterKeys(k, "")
			}
		}
	}
	return nil
}

// sqlOnlyTreeAction reports whether a on n is about tables (so refused on
// redis); editing and deleting a connection node are not.
func sqlOnlyTreeAction(a keymap.Action, n *tree.Node) bool {
	if n != nil && n.Kind == tree.KindRedisKey && a == keymap.RenameTable {
		return false
	}
	if n != nil && a == keymap.DeleteNode && (n.Kind == tree.KindRedisKey || n.Kind == tree.KindRedisDB || n.Kind == tree.KindKeyFolder) {
		return false
	}
	if a == keymap.NodeActions && n != nil && (n.Kind == tree.KindConn || isRedisNode(n)) {
		return false // the menu lists only what the node can do
	}
	switch a {
	case keymap.ImportData, keymap.MarkCompare, keymap.Compare, keymap.NodeActions,
		keymap.TruncateTable, keymap.RenameTable:
		return true
	case keymap.EditNode, keymap.DeleteNode:
		return n != nil && n.Kind != tree.KindConn
	}
	return false
}

// canChange reports whether conn may be edited or deleted: not while a
// console on it holds an open transaction or a table on it has unsaved
// changes.
func (m *Model) canChange(conn string) bool {
	for _, t := range m.tabs {
		switch t := t.(type) {
		case txHolder:
			if c, _ := t.Target(); c == conn && t.openTx() {
				m.setStatus(openTxNote(conn, t), true)
				return false
			}
		case stager:
			if c, _ := t.Target(); c == conn && t.changes() > 0 {
				m.setStatus(joinHints(t.Title()+" on "+conn+" has unsaved changes", t.unsavedHint("submits", "discards")), true)
				return false
			}
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
	if msg.node.Kind == tree.KindConn && msg.err == nil {
		m.dropScans(msg.node.Conn) // its db nodes are new
	}
	m.tree.SetChildren(msg.node, msg.children, msg.err)
	if msg.err != nil {
		m.setStatus(msg.err.Error(), true)
		return m.continueReveal(msg)
	}
	return tea.Batch(m.ensureMeta(msg.node.Conn, msg.node.NS.Database), m.continueReveal(msg))
}

func (m *Model) passwordSubmitted(msg overlay.PasswordSubmittedMsg) tea.Cmd {
	if err := m.backend.SetPassword(msg.Conn, msg.Password, msg.Save); err != nil {
		m.setStatus("password kept for this session only: "+err.Error(), true)
	}
	node, tabID := m.retryNode, m.retryTab
	m.retryNode, m.retryTab = nil, 0
	retry := m.retryFailedMeta(msg.Conn)
	if t, ok := m.tabByID(tabID).(passwordRetrier); ok {
		if usesConn(m.tabByID(tabID), msg.Conn) {
			return tea.Batch(retry, t.retryAfterPassword())
		}
	}
	if node == nil || node.Conn != msg.Conn {
		m.tree.ResetConn(msg.Conn)
		return retry
	}
	if node.Kind == tree.KindRedisDB {
		return tea.Batch(retry, m.refreshKeys(node))
	}
	m.tree.StartLoad(node)
	return tea.Batch(retry, loadChildren(m.backend, node))
}

// filterSearcher is a tab whose n / N search with its filter while one is
// set.
type filterSearcher interface {
	searchFilter(forward bool) (tea.Cmd, bool)
}

// connUser is a tab that works on more connections than its Target.
type connUser interface{ usesConn(conn string) bool }

// usesConn reports whether t works on conn.
func usesConn(t tab, conn string) bool {
	if u, ok := t.(connUser); ok {
		return u.usesConn(conn)
	}
	c, _ := t.Target()
	return c == conn
}

// askPassword opens the password prompt when a tab could not load or run
// for want of one; the tab tries again once it is entered. (The tree asks
// in childrenLoaded.)
func (m *Model) askPassword(msg tea.Msg, conn string, err error) {
	if !errors.Is(err, session.ErrPasswordRequired) || m.overlay != nil {
		return
	}
	switch msg := msg.(type) {
	case pageLoadedMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case structureLoadedMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case runDoneMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case redisRunMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case editsAppliedMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case diffLoadedMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case keyLoadedMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case keyAppliedMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case serverInfoMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case serverLoadedMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case sqlServerInfoMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case sqlServerSampleMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case sqlServerLoadedMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case sessionActedMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case bigKeysStepMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
	case kafkaMsg:
		m.retryNode, m.retryTab = nil, msg.TabID()
	default:
		return
	}
	m.overlay = overlay.NewPassword(conn)
}

func (m *Model) runCommand(text string) tea.Cmd {
	cmd := strings.TrimSpace(text)
	if opt, ok := strings.CutPrefix(cmd, "set "); ok {
		return m.setOption(strings.TrimSpace(opt))
	}
	verb, arg, _ := strings.Cut(cmd, " ")
	arg = strings.TrimSpace(arg)
	switch verb {
	case "e", "edit", "w", "write", "w!", "write!", "wq":
		if _, ok := m.activeTab().(*redisConsoleTab); ok && m.focus == focusTabs || verb[0] == 'e' && m.refuseRedis(m.contextConn()) {
			m.setStatus(errRedisUnsupported.Error(), true)
			return nil
		}
	}
	if verb == "e" || verb == "edit" {
		return m.editFile(arg)
	}
	switch verb {
	case "w", "write", "w!", "write!", "wq":
		c, _ := m.activeTab().(*consoleTab)
		if arg != "" || verb != "w" && verb != "write" || c != nil && c.file != nil {
			return m.writeFile(arg, strings.HasSuffix(verb, "!"), verb == "wq")
		}
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
	case "w", "write":
		if s, ok := m.activeTab().(stager); ok {
			// A console is a stager only while its result has staged changes.
			if _, console := s.(*consoleTab); !console || s.changes() > 0 {
				return s.submit()
			}
		}
		m.setStatus("nothing to write: the active tab has no staged changes to submit", true)

	case "locate":
		return m.locate()
	case "commit", "rollback":
		if _, ok := m.activeTab().(*redisConsoleTab); ok {
			m.setStatus(errRedisTx, true)
			return nil
		}
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
	if c, ok := t.(*consoleTab); ok && c.dirty() && !force {
		lines := []string{c.file.name() + " has unsaved changes."}
		if c.openTx() {
			lines = append(lines, "It also has an open transaction ("+c.TxStatus()+"), which closing rolls back.")
		}
		m.overlay = overlay.NewConfirm("Unsaved changes", append(lines, "Closing the tab discards them."), "", closeTabConfirmedMsg{tabID: t.ID()})
		return nil
	}
	if c, ok := t.(txHolder); ok && c.openTx() && !force {
		m.overlay = overlay.NewConfirm("Open transaction", []string{
			c.Title() + " has an open transaction (" + c.TxStatus() + ").",
			"Closing the console rolls it back.",
		}, "", closeTabConfirmedMsg{tabID: t.ID()})
		return nil
	}
	if d, ok := t.(stager); ok && d.changes() > 0 && !force {
		m.overlay = overlay.NewConfirm("Unsaved changes", []string{
			d.Title() + " has " + plural(d.changes(), "unsaved change") + ".",
			"Closing the tab discards them.",
		}, "", closeTabConfirmedMsg{tabID: t.ID()})
		return nil
	}
	return m.closeTab(m.active)
}

// requestQuit quits, asking first when consoles hold open transactions
// (unless force).
func (m *Model) requestQuit(force bool) tea.Cmd {
	open, edits := m.openTransactions(), m.unsavedTables()
	if force || len(open)+len(edits) == 0 {
		return m.quit()
	}
	if len(edits) == 0 {
		m.overlay = overlay.NewConfirm("Open transactions", []string{
			"Uncommitted transactions in: " + strings.Join(open, ", ") + ".",
			"Quitting rolls them back.",
		}, "", quitConfirmedMsg{})
		return nil
	}
	lines := []string{"Unsaved changes in: " + strings.Join(edits, ", ") + "."}
	if len(open) > 0 {
		lines = append(lines, "Uncommitted transactions in: "+strings.Join(open, ", ")+".")
	}
	m.overlay = overlay.NewConfirm("Unsaved changes", append(lines, "Quitting discards them."), "", quitConfirmedMsg{})
	return nil
}

// unsavedTables names the tabs with staged changes ("users (3)") and the
// unsaved files.
func (m *Model) unsavedTables() []string {
	var out []string
	for _, t := range m.tabs {
		if d, ok := t.(stager); ok && d.changes() > 0 {
			out = append(out, fmt.Sprintf("%s (%d)", d.Title(), d.changes()))
		}
		if c, ok := t.(*consoleTab); ok && c.dirty() {
			out = append(out, c.file.name())
		}
	}
	return out
}

// openTxNote says that a console on conn holds a transaction, and what ends it.
func openTxNote(conn string, t txHolder) string {
	return "a console on " + conn + " has an open transaction · " + t.endTxHint()
}

// openTransactions names the consoles with an uncommitted transaction.
func (m *Model) openTransactions() []string {
	var out []string
	for _, t := range m.tabs {
		if c, ok := t.(txHolder); ok && c.openTx() {
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
		if c, ok := t.(txHolder); ok && c.openTx() {
			if conn, _ := c.Target(); conn == cfg.Name {
				m.setStatus(openTxNote(cfg.Name, c), true)
				return nil
			}
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

func (m *Model) openTab(key string, rec history.Recent, create func(id int) tab) tea.Cmd {
	return tea.Batch(m.showTab(key, create), recordRecent(m.store, rec))
}

// showTab switches to the tab with key, creating it when there is none,
// without recording it among the recent tabs.
func (m *Model) showTab(key string, create func(id int) tab) tea.Cmd {
	for i, t := range m.tabs {
		if t.Key() == key {
			m.gotoTab(i)
			return nil
		}
	}
	m.nextTabID++
	t := create(m.nextTabID)
	if r, ok := t.(registerUser); ok {
		r.UseRegisters(m.regs)
	}
	m.tabs = append(m.tabs, t)
	m.active = len(m.tabs) - 1
	m.focus = focusTabs
	m.layout()
	return t.Init()
}

// openDiff opens, or switches to, the tab comparing src with tgt.
func (m *Model) openDiff(src, tgt compareSide) tea.Cmd {
	return m.showTab(diffTabKey(src, tgt), func(id int) tab {
		t := newDiffTab(id, m.backend, src, tgt)
		t.keys = m.keys
		if m.settings.CompareMaxRows > 0 {
			t.maxRows = m.settings.CompareMaxRows
		}
		return t
	})
}

func (m *Model) openData(conn string, ref db.TableRef) tea.Cmd {
	return tea.Batch(m.openTab(dataTabKey(conn, ref), history.Recent{Kind: history.RecentData, Conn: conn, Database: ref.Database, Schema: ref.Schema, Name: ref.Name}, func(id int) tab {
		t := newDataTab(id, m.backend, m.store, conn, ref, m.settings)
		t.keys = m.keys
		return t
	}), m.ensureMeta(conn, ref.Database))
}

func (m *Model) openStructure(conn string, ref db.TableRef) tea.Cmd {
	return tea.Batch(m.openTab(structureTabKey(conn, ref), history.Recent{Kind: history.RecentStructure, Conn: conn, Database: ref.Database, Schema: ref.Schema, Name: ref.Name}, func(id int) tab {
		t := newStructureTab(id, m.backend, m.store, conn, ref)
		t.keys = m.keys
		return t
	}), m.ensureMeta(conn, ref.Database))
}

func (m *Model) openColumnsFromContext() tea.Cmd {
	if c, ok := m.activeTab().(*consoleTab); ok && m.focus == focusTabs {
		return m.openResultColumns(c)
	}
	conn, ref, ok := m.contextTable()
	if !ok {
		return nil
	}
	return m.openColumns(conn, ref)
}

// pasteTab is a console: it takes terminal pastes and clipboard reads.
type pasteTab interface {
	tab
	Paste(msg tea.PasteMsg) tea.Cmd
}

// keyTakingTab returns the focused tab when it reads keys before the
// keymap (a console's vim editor in NORMAL and VISUAL mode).
func (m *Model) keyTakingTab() keyTaker {
	if m.focus != focusTabs {
		return nil
	}
	if t, ok := m.activeTab().(keyTaker); ok {
		return t
	}
	return nil
}

// jumpTab is gt / gT / H / L: to tab j.To (1-based), or j.By tabs on, wrapping.
func (m *Model) jumpTab(j vimedit.TabJump) tea.Cmd {
	n := len(m.tabs)
	switch {
	case n == 0 || j.To > n:
		return nil
	case j.To > 0:
		m.gotoTab(j.To - 1)
	default:
		m.gotoTab(((m.active+j.By)%n + n) % n)
	}
	return m.touchActive()
}

// withEditorHelp adds the vim editor's keys after the console's bindings.
func withEditorHelp(entries []keymap.HelpEntry) []keymap.HelpEntry {
	i := 0
	for i < len(entries) && entries[i].Context == keymap.Console {
		i++
	}
	var vim []keymap.HelpEntry
	for _, h := range vimedit.Help() {
		vim = append(vim, keymap.HelpEntry{Context: keymap.Console, Keys: h.Keys, Desc: h.Desc})
	}
	return slices.Concat(entries[:i], vim, entries[i:])
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
	if md, ok := m.overlay.(moder); ok {
		return md.Mode() // the cell editor
	}
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
	return tea.Batch(m.openTab(consoleTabKey(conn, ns), history.Recent{Kind: history.RecentConsole, Conn: conn, Database: ns.Database, Schema: ns.Schema}, func(id int) tab {
		t := newConsoleTab(id, m.backend, m.store, m.catalogs, conn, ns, m.settings, m.timings)
		t.keys = m.keys
		t.res.keys = m.keys
		return t
	}), m.ensureMeta(conn, ns.Database))
}

// scriptConsoleMsg asks for a new console on conn holding text.
type scriptConsoleMsg struct {
	conn string
	ns   db.Namespace
	text string
}

// openScriptConsole opens a new console on conn with text, apart from the
// connection's usual console; it keeps no draft.
func (m *Model) openScriptConsole(conn string, ns db.Namespace, text string) tea.Cmd {
	key := consoleTabKey(conn, ns) + "\x00script\x00" + strconv.Itoa(m.nextTabID+1)
	var c *consoleTab
	open := m.showTab(key, func(id int) tab {
		c = newConsoleTab(id, m.backend, m.store, m.catalogs, conn, ns, m.settings, m.timings)
		c.keys, c.scratchKey = m.keys, key
		c.res.keys = m.keys
		return c
	})
	return tea.Batch(open, c.insertText(text), m.ensureMeta(conn, ns.Database))
}

// editFile carries out :e path on the connection in context.
func (m *Model) editFile(arg string) tea.Cmd {
	if arg == "" {
		m.setStatus(":e needs a file name", true)
		return nil
	}
	path, err := absPath(arg)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	conn, ns := m.contextNamespace()
	if conn == "" {
		m.setStatus("open a console or select a connection first", true)
		return nil
	}
	return m.openFile(conn, ns, path)
}

// openFile switches to path's console on conn, or reads path for a new one.
func (m *Model) openFile(conn string, ns db.Namespace, path string) tea.Cmd {
	key := fileTabKey(path, conn, ns)
	for i, t := range m.tabs {
		if t.Key() == key {
			m.gotoTab(i)
			return nil
		}
	}
	return readSQLFile(conn, ns, path)
}

// fileRead opens the console for a file openFile read.
func (m *Model) fileRead(msg fileReadMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(msg.err.Error(), true)
		return nil
	}
	f := msg.file
	rec := history.Recent{Kind: history.RecentFile, Conn: msg.conn, Database: msg.ns.Database, Schema: msg.ns.Schema, Name: f.path}
	return tea.Batch(m.openTab(fileTabKey(f.path, msg.conn, msg.ns), rec, func(id int) tab {
		c := newConsoleTab(id, m.backend, m.store, m.catalogs, msg.conn, msg.ns, m.settings, m.timings)
		c.keys, c.res.keys = m.keys, m.keys
		c.file = &f
		c.editor.SetValue(f.saved)
		return c
	}), m.ensureMeta(msg.conn, msg.ns.Database))
}

// writeFile carries out :w [path], :w! [path] and :wq in a console.
func (m *Model) writeFile(arg string, force, quit bool) tea.Cmd {
	c, ok := m.activeTab().(*consoleTab)
	if !ok {
		m.setStatus("only a console can be written to a file", true)
		return nil
	}
	if arg == "" && c.file == nil {
		if quit {
			m.setStatus(":wq needs a file · :w <path> first", true)
			return nil
		}
		return m.runCommand("w") // :w! in a plain console: nothing to force
	}
	path := ""
	if arg != "" {
		p, err := absPath(arg)
		if err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		if key := fileTabKey(p, c.conn, c.ns); key != c.Key() {
			for _, o := range m.tabs {
				if o.Key() == key {
					m.setStatus(tildePath(p)+" is open in another tab", true)
					return nil
				}
			}
		}
		path = p
	}
	return c.save(path, force, quit)
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
	if c, ok := m.activeTab().(*redisConsoleTab); ok && m.focus == focusTabs {
		return c.insertText(e.SQL)
	}
	if _, ok := m.backend.Connection(e.Conn); !ok {
		m.setStatus("connection "+e.Conn+" no longer exists", true)
		return nil
	}
	if isRedis(m.backend, e.Conn) {
		n, err := strconv.Atoi(e.Database)
		if err != nil {
			n = -1
		}
		open := m.openRedisConsole(e.Conn, n)
		c := m.activeTab().(*redisConsoleTab)
		return tea.Batch(open, c.insertText(e.SQL))
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
	m.tree.AddRoot(connNode(c))
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
	m.tree.ReplaceRoot(msg.old, connNode(c))
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

// closeTabsOf closes every tab on conn, except file consoles with unsaved
// changes: those only lose their session, so the text is not lost.
func (m *Model) closeTabsOf(conn string) tea.Cmd {
	var cmds []tea.Cmd
	for i := len(m.tabs) - 1; i >= 0; i-- {
		if c, _ := m.tabs[i].Target(); c != conn {
			continue
		}
		if fc, ok := m.tabs[i].(*consoleTab); ok && fc.dirty() {
			cmds = append(cmds, shutdownCmd(fc))
			fc.clearTx()
			fc.setStatus("connection "+conn+" changed: its session is closed, the file stays open · :w to save", true)
			continue
		}
		cmds = append(cmds, m.closeTab(i))
	}
	return tea.Batch(cmds...)
}

// ---- layout and rendering ----

// treeWidth is the width the tree is drawn at: all of the screen when it
// is full screen, none when the tabs are.
func (m *Model) treeWidth() int {
	switch {
	case m.full && m.focus == focusTree:
		return m.width
	case m.full:
		return 0
	}
	return m.sideWidth()
}

// sideWidth is the width of the tree beside the tabs.
func (m *Model) sideWidth() int {
	if !m.showTree {
		return 0
	}
	return min(textutil.Clamp(m.width/4, 24, 40), m.width)
}

func (m *Model) bodyHeight() int { return max(m.height-1, 0) }

func (m *Model) layout() {
	inner := max(m.bodyHeight()-2, 0)
	m.tree.SetHeight(inner)
	side := m.sideWidth() // behind the full-screen tree the tabs keep their size
	if m.full && m.focus == focusTabs {
		side = 0
	}
	tabsWidth := max(m.width-side-2, 0)
	m.laidFocus = m.focus
	for _, t := range m.tabs {
		t.SetSize(tabsWidth, inner)
	}
}

// fuller is a tab split in two panes (a console): full screen, it shows
// only the focused one.
type fuller interface {
	SetFull(full bool)
}

// syncFull keeps the layout in step with full screen, which follows the
// focus from the tree to the tabs and, in a console, from the editor to the
// results.
func (m *Model) syncFull() {
	if m.full && m.focus != m.laidFocus {
		m.layout()
	}
	if f, ok := m.activeTab().(fuller); ok {
		f.SetFull(m.full)
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
			body = theme.Muted.Render("No connections\n" + keyHint(m.keys, keymap.Tree, keymap.NewConnection, "add one"))
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
		box := overlay.WhichKey(m.keys.Continuations(m.context(), m.connKind(), m.pending))
		x := max(m.width-lipgloss.Width(box), 0)
		y := max(m.height-1-lipgloss.Height(box), 0)
		screen = overlay.PlaceAt(screen, box, x, y)
	}
	return screen
}

func (m *Model) renderTabs(width, height int) string {
	t := m.activeTab()
	if t == nil {
		return theme.Box(m.title(), welcomeText(m.keys), width, height, false)
	}
	focused := m.focus == focusTabs
	conn, _ := t.Target()
	cfg, _ := m.backend.Connection(conn)
	danger := cfg.Env == config.EnvProd
	if f, ok := t.(framer); ok {
		return f.Framed(m.tabBar(), focused, danger)
	}
	return theme.Frame(m.tabBar(), t.View(focused), width, height, focused, danger)
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
	if m.workspace != "" {
		parts = append(parts, theme.Accent.Render("["+textutil.Sanitize(m.workspace)+"]"))
	}
	if prod {
		parts = append(parts, theme.Prod.Render("[PROD]"))
	}
	if ok && cfg.IsReadOnly() {
		parts = append(parts, theme.Muted.Render("RO"))
	}
	if m.full {
		parts = append(parts, theme.Accent.Render("FULL"))
	}
	if c, ok := m.activeTab().(txHolder); ok {
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
	fullHint := ""
	if m.full {
		fullHint = keyHint(m.keys, keymap.Global, keymap.ExitFullScreen, "leave full screen")
	}
	right := theme.Muted.Render(joinHints(
		m.keys.Display([]string{m.keys.Leader()})+" leader",
		fullHint,
		keyHint(m.keys, keymap.Global, keymap.Help, "help"),
		keyHint(m.keys, keymap.Global, keymap.CommandLine, "command"),
	))
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
	loc := strings.Join(parts, "/")
	if c, ok := m.activeTab().(*consoleTab); ok && m.focus == focusTabs && c.file != nil {
		loc += "  " + shortPath(c.file.path, 40)
	}
	return loc
}
