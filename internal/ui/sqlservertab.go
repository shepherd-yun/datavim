package ui

import (
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/panes/sparkline"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// sqlSection is one part of a SQL connection's server tab.
type sqlSection int

const (
	sqlStatus sqlSection = iota
	sqlSessions
	sqlLocks
	sqlSlow
	sqlSettings
	sqlReplication
	sqlSections
)

// openSQLServer opens (or shows) the server tab of SQL connection conn.
func (m *Model) openSQLServer(conn string) tea.Cmd {
	return m.showTab(serverTabKey(conn), func(id int) tab {
		return newSQLServerTab(id, m.backend, m.keys, conn, m.timings.server, m.settings)
	})
}

type sqlServerInfoMsg struct {
	tabID int
	conn  string
	info  db.ServerInfo
	err   error
}

func (m sqlServerInfoMsg) TabID() int               { return m.tabID }
func (m sqlServerInfoMsg) failure() (string, error) { return m.conn, m.err }

type sqlServerSampleMsg struct {
	tabID, gen int
	conn       string
	sample     db.ServerSample
	err        error
}

func (m sqlServerSampleMsg) TabID() int               { return m.tabID }
func (m sqlServerSampleMsg) failure() (string, error) { return m.conn, m.err }

type sqlServerTickMsg struct{ tabID, gen int }

func (m sqlServerTickMsg) TabID() int { return m.tabID }

// sqlServerLoadedMsg is one table section; sessions is set for Sessions.
type sqlServerLoadedMsg struct {
	tabID    int
	conn     string
	sec      sqlSection
	rs       *db.ResultSet
	sessions db.SessionList
	err      error
}

func (m sqlServerLoadedMsg) TabID() int               { return m.tabID }
func (m sqlServerLoadedMsg) failure() (string, error) { return m.conn, m.err }

// sqlServerFilterMsg is the text typed to filter the settings by name.
type sqlServerFilterMsg struct {
	tabID int
	text  string
}

func (m sqlServerFilterMsg) TabID() int { return m.tabID }

// sessionConfirmedMsg is a confirmed cancel or disconnect.
type sessionConfirmedMsg struct {
	tabID int
	act   sessionAction
	id    db.SessionID
}

func (m sessionConfirmedMsg) TabID() int { return m.tabID }

type sessionActedMsg struct {
	tabID int
	conn  string
	act   sessionAction
	id    db.SessionID
	ok    bool
	err   error
}

func (m sessionActedMsg) TabID() int               { return m.tabID }
func (m sessionActedMsg) failure() (string, error) { return m.conn, m.err }

// topStatements is how many statements the slow queries section reads.
const topStatements = 50

// sqlServerTab is a SQL connection's server tab: the status (sampled every
// interval while it is on screen) and the table sections, which share one
// grid and are read when shown or refreshed.
type sqlServerTab struct {
	id       int
	backend  Backend
	keys     *keymap.Keymap
	conn     string
	database string
	interval time.Duration
	section  sqlSection

	info     db.ServerInfo
	haveInfo bool
	infoErr  error

	visible bool
	running bool
	gen     int // tells the current sampling from stopped ones

	samples   []db.ServerSample
	statusErr error

	grid     *grid.Model
	gridSec  sqlSection // the section the grid holds; sqlSections: none
	secErr   error
	loading  bool
	sessions db.SessionList // as read: the grid's rows, row by row
	showIdle bool
	settings *db.ResultSet // as read, before the filter
	filter   string
	status   string
	statusE  bool

	width, height int
}

func newSQLServerTab(id int, b Backend, keys *keymap.Keymap, conn string, interval time.Duration, s config.Settings) *sqlServerTab {
	cfg, _ := b.Connection(conn)
	database := cfg.Database
	if database == "" {
		database = sqlx.DialectOf(cfg.Driver).Features().FallbackDatabase
	}
	return &sqlServerTab{
		id: id, backend: b, keys: keys, conn: conn, database: database, interval: interval,
		grid: grid.New(nil, s.MaxColumnWidth), gridSec: sqlSections,
	}
}

func (t *sqlServerTab) ID() int                 { return t.id }
func (t *sqlServerTab) Title() string           { return t.conn + " · server" }
func (t *sqlServerTab) Key() string             { return serverTabKey(t.conn) }
func (t *sqlServerTab) Context() keymap.Context { return keymap.DBServer }
func (t *sqlServerTab) Init() tea.Cmd           { return t.loadInfo() } // syncShown starts the sampling
func (t *sqlServerTab) SetSize(w, h int)        { t.width, t.height = max(w, 0), max(h, 0) }

func (t *sqlServerTab) Target() (string, db.TableRef) { return t.conn, db.TableRef{} }

func (t *sqlServerTab) activeGrid() *grid.Model { return t.grid }

func (t *sqlServerTab) setStatus(s string, isErr bool) { t.status, t.statusE = s, isErr }

func (t *sqlServerTab) Find(q string, forward bool) bool {
	return t.section != sqlStatus && t.grid.Find(q, forward)
}

func (t *sqlServerTab) Shown() tea.Cmd {
	t.visible = true
	return t.startSampling()
}

func (t *sqlServerTab) Hidden() {
	t.visible = false
	t.stopSampling()
}

func (t *sqlServerTab) retryAfterPassword() tea.Cmd {
	if t.section != sqlStatus {
		return tea.Batch(t.loadInfo(), t.load())
	}
	t.stopSampling()
	return tea.Batch(t.loadInfo(), t.startSampling())
}

func (t *sqlServerTab) exportSource() exportSrc {
	if t.section == sqlStatus || t.gridSec != t.section || t.grid.Result() == nil {
		return exportSrc{}
	}
	name := [sqlSections]string{"", "sessions", "locks", "statements", "settings", "replication"}[t.section]
	return exportSrc{rs: t.grid.Result(), name: name, conn: t.conn}
}

// load reads the current table section.
func (t *sqlServerTab) load() tea.Cmd {
	t.loading = true
	id, conn, sec, idle := t.id, t.conn, t.section, t.showIdle
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := sqlServerLoadedMsg{tabID: id, conn: conn, sec: sec}
		msg.err = t.withServer(ctx, func(s db.ServerReader) error {
			var err error
			switch sec {
			case sqlSessions:
				msg.sessions, err = s.Sessions(ctx, idle)
				msg.rs = msg.sessions.Rows
			case sqlLocks:
				msg.rs, err = s.Locks(ctx)
			case sqlSlow:
				msg.rs, err = s.TopStatements(ctx, topStatements)
			case sqlSettings:
				msg.rs, err = s.Settings(ctx)
			case sqlReplication:
				msg.rs, err = s.Replication(ctx)
			case sqlStatus, sqlSections:
			}
			return err
		})
		return msg
	}
}

// marked is the sessions with "*" before the first cell of datavim's own.
func marked(l db.SessionList) *db.ResultSet {
	if l.Rows == nil {
		return nil
	}
	rs := &db.ResultSet{Columns: l.Rows.Columns, Rows: make([][]db.Value, len(l.Rows.Rows))}
	for i, r := range l.Rows.Rows {
		rs.Rows[i] = r
		if i < len(l.Self) && l.Self[i] && len(r) > 0 {
			rs.Rows[i] = append([]db.Value{{Text: "*" + r[0].Text}}, r[1:]...)
		}
	}
	return rs
}

// filtered is the settings whose name (first column) contains t.filter,
// ignoring case.
func (t *sqlServerTab) filtered() *db.ResultSet {
	if t.filter == "" || t.settings == nil {
		return t.settings
	}
	want := strings.ToLower(t.filter)
	rs := &db.ResultSet{Columns: t.settings.Columns}
	for _, r := range t.settings.Rows {
		if len(r) > 0 && strings.Contains(strings.ToLower(r[0].Text), want) {
			rs.Rows = append(rs.Rows, r)
		}
	}
	return rs
}

// show puts rs in the grid for the current section, keeping the cursor
// when the grid already held it.
func (t *sqlServerTab) show(rs *db.ResultSet) {
	if t.gridSec == t.section {
		t.grid.ReplaceResult(rs)
	} else {
		t.grid.SetResult(rs)
	}
	t.gridSec = t.section
}

// withServer runs fn on the connection's server reader.
func (t *sqlServerTab) withServer(ctx context.Context, fn func(db.ServerReader) error) error {
	return withConn(ctx, t.backend, t.conn, t.database, func(c db.Conn) error { return fn(c.Server()) })
}

func (t *sqlServerTab) loadInfo() tea.Cmd {
	id, conn := t.id, t.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := sqlServerInfoMsg{tabID: id, conn: conn}
		msg.err = t.withServer(ctx, func(s db.ServerReader) error {
			var err error
			msg.info, err = s.Info(ctx)
			return err
		})
		return msg
	}
}

// startSampling takes the first sample when the status section is on
// screen and not already sampling.
func (t *sqlServerTab) startSampling() tea.Cmd {
	if !t.visible || t.section != sqlStatus || t.running {
		return nil
	}
	t.gen++
	t.running = true
	return t.sample()
}

// stopSampling drops the pending tick and sample: they carry an old gen.
func (t *sqlServerTab) stopSampling() {
	if t.running {
		t.gen++
		t.running = false
	}
}

func (t *sqlServerTab) sample() tea.Cmd {
	id, gen, conn := t.id, t.gen, t.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := sqlServerSampleMsg{tabID: id, gen: gen, conn: conn}
		msg.err = t.withServer(ctx, func(s db.ServerReader) error {
			var err error
			msg.sample, err = s.Sample(ctx)
			return err
		})
		return msg
	}
}

func (t *sqlServerTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case sqlServerInfoMsg:
		t.infoErr = m.err
		if m.err == nil {
			t.info, t.haveInfo = m.info, true
		}
	case sqlServerSampleMsg:
		if m.gen != t.gen || !t.running {
			return nil
		}
		if m.err != nil {
			t.stopSampling()
			t.statusErr = m.err
			return nil
		}
		t.statusErr = nil
		t.samples = append(t.samples, m.sample)
		if over := len(t.samples) - statusSamples; over > 0 {
			t.samples = t.samples[over:]
		}
		id, gen := t.id, t.gen
		return tea.Tick(t.interval, func(time.Time) tea.Msg { return sqlServerTickMsg{tabID: id, gen: gen} })
	case sqlServerTickMsg:
		if m.gen == t.gen && t.running {
			return t.sample()
		}
	case sqlServerLoadedMsg:
		if m.sec != t.section {
			return nil
		}
		t.loading = false
		if m.err != nil {
			t.secErr = m.err
			t.grid.SetResult(nil)
			t.gridSec, t.sessions = sqlSections, db.SessionList{}
			return nil
		}
		t.secErr = nil
		rs := m.rs
		switch m.sec {
		case sqlSessions:
			t.sessions = m.sessions
			rs = marked(m.sessions)
		case sqlSettings:
			t.settings = m.rs
			rs = t.filtered()
		case sqlStatus, sqlLocks, sqlSlow, sqlReplication, sqlSections:
		}
		t.show(rs)
	case sessionConfirmedMsg:
		return t.act(m.act, m.id)
	case sessionActedMsg:
		switch {
		case errors.Is(m.err, db.ErrSessionChanged):
			t.setStatus("session "+m.id.ID+" changed since the list was read; refreshed", true)
		case m.err != nil:
			t.setStatus(m.err.Error(), true)
			return nil
		case !m.ok:
			t.setStatus("session "+m.id.ID+" is already gone", false)
		case m.act == actCancel:
			t.setStatus("cancelled the query of session "+m.id.ID, false)
		default:
			t.setStatus("disconnected session "+m.id.ID, false)
		}
		if t.section == sqlSessions {
			return t.load()
		}
	case sqlServerFilterMsg:
		t.filter = strings.TrimSpace(m.text)
		if t.section == sqlSettings && t.gridSec == sqlSettings {
			t.show(t.filtered())
		}
	}
	return nil
}

func (t *sqlServerTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	switch a {
	case keymap.NextSection:
		return t.setSection((t.section + 1) % sqlSections)
	case keymap.PrevSection:
		return t.setSection((t.section + sqlSections - 1) % sqlSections)
	case keymap.Refresh:
		if t.section == sqlStatus {
			t.samples, t.statusErr = nil, nil
			t.stopSampling()
			return tea.Batch(t.loadInfo(), t.startSampling())
		}
		if !t.loading {
			return tea.Batch(t.loadInfo(), t.load())
		}
		return nil
	case keymap.KillClient:
		return t.ask(actDisconnect)
	case keymap.CancelSessionQuery:
		return t.ask(actCancel)
	case keymap.ToggleIdle:
		if t.section != sqlSessions {
			t.setStatus("works in the sessions list", true)
			return nil
		}
		t.showIdle = !t.showIdle
		return t.load()
	case keymap.FilterPrompt:
		if t.section != sqlSettings {
			t.setStatus("filter works in the "+strings.ToLower(t.sectionNames()[sqlSettings])+" list", true)
			return nil
		}
		id := t.id
		return openOverlay(overlay.NewPrompt("Filter "+strings.ToLower(t.sectionNames()[sqlSettings]), "Name contains", t.filter, func(s string) tea.Msg {
			return sqlServerFilterMsg{tabID: id, text: s}
		}))
	case keymap.FilterClear:
		if t.section == sqlSettings && t.filter != "" {
			t.filter = ""
			if t.gridSec == sqlSettings {
				t.show(t.filtered())
			}
		}
		return nil
	}
	if t.section != sqlStatus {
		if cmd, ok := gridAction(t.grid, a); ok {
			return cmd
		}
	}
	return nil
}

// ask checks a cancel or disconnect of the session under the cursor with
// guardSession and asks for it.
func (t *sqlServerTab) ask(a sessionAction) tea.Cmd {
	if t.section != sqlSessions {
		t.setStatus("works in the sessions list", true)
		return nil
	}
	r, _ := t.grid.Position()
	l := t.sessions
	if t.gridSec != sqlSessions || r >= len(l.IDs) || r >= len(l.Info) {
		t.setStatus("no session selected", true)
		return nil
	}
	if !t.haveInfo {
		// CanCancel is unknown
		t.setStatus("server info is not loaded; "+t.keys.KeysFor(keymap.Global, keymap.Refresh)+" reads it again", true)
		return nil
	}
	if a == actCancel && !t.info.CanCancel {
		alt := "disconnect instead"
		if k := t.keys.KeysFor(keymap.DBServer, keymap.KillClient); k != "" {
			alt = "use " + k + " to disconnect"
		}
		t.setStatus("cancelling a query is not supported here; "+alt, true)
		return nil
	}
	in := l.Info[r]
	target := sessionTarget{
		ID: l.IDs[r].ID, User: in.User, Host: in.Host, DB: in.DB, State: in.State, Elapsed: in.Elapsed, SQL: in.SQL,
		Self: r < len(l.Self) && l.Self[r],
	}
	cfg, _ := t.backend.Connection(t.conn)
	chk := guardSession(cfg, a, target, max(min(t.width, 80)-8, 20))
	if chk.Refuse != "" {
		t.setStatus(chk.Refuse, true)
		return nil
	}
	return openOverlay(overlay.NewConfirm(chk.Title, chk.Body, chk.Expect, sessionConfirmedMsg{tabID: t.id, act: a, id: l.IDs[r]}))
}

func (t *sqlServerTab) act(a sessionAction, id db.SessionID) tea.Cmd {
	tabID, conn := t.id, t.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := sessionActedMsg{tabID: tabID, conn: conn, act: a, id: id}
		msg.err = t.withServer(ctx, func(s db.ServerReader) error {
			var err error
			if a == actCancel {
				msg.ok, err = s.CancelQuery(ctx, id)
			} else {
				msg.ok, err = s.KillSession(ctx, id)
			}
			return err
		})
		return msg
	}
}

func (t *sqlServerTab) setSection(s sqlSection) tea.Cmd {
	if s == t.section {
		return nil
	}
	t.section = s
	t.grid.SetResult(nil)
	t.gridSec, t.secErr, t.sessions = sqlSections, nil, db.SessionList{}
	if s != sqlStatus {
		t.stopSampling()
		return t.load()
	}
	return t.startSampling()
}

func (t *sqlServerTab) sectionNames() []string {
	settings := t.info.SettingsTitle
	if settings == "" {
		settings = "Variables"
	}
	return []string{"Status", "Sessions", "Locks", "Slow queries", settings, "Replication"}
}

// errLine shows err: an unavailable part muted (it is the server's
// configuration, not a failure), anything else as an error.
func errLine(err error) string {
	if errors.Is(err, db.ErrUnavailable) {
		return theme.Muted.Render(textutil.Sanitize(err.Error()))
	}
	return theme.Error.Render(textutil.Sanitize(err.Error()))
}

func (t *sqlServerTab) View(focused bool) string {
	lines := []string{sectionBar(t.sectionNames(), int(t.section), t.width)}
	if t.status != "" {
		st := theme.Muted
		if t.statusE {
			st = theme.Error
		}
		lines = append(lines, st.Render(textutil.Sanitize(t.status)))
	}
	if t.infoErr != nil {
		lines = append(lines, errLine(t.infoErr))
	}
	body := ""
	switch {
	case t.section == sqlStatus:
		lines = append(lines, t.statusLines()...)
	case t.secErr != nil:
		lines = append(lines, errLine(t.secErr))
	case t.gridSec != t.section:
		lines = append(lines, theme.Muted.Render("loading…"))
	default:
		if l := t.summary(); l != "" {
			lines = append(lines, theme.Muted.Render(l))
		}
		if t.grid.RowCount() == 0 {
			lines = append(lines, theme.Muted.Render(t.emptyText()))
			break
		}
		t.grid.SetSize(t.width, max(t.height-len(lines), 0))
		body = t.grid.View(focused)
	}
	for i, l := range lines {
		lines[i] = textutil.Fit(l, t.width)
	}
	if body != "" {
		lines = append(lines, body)
	}
	return strings.Join(lines, "\n")
}

// summary is the line above a section's table.
func (t *sqlServerTab) summary() string {
	switch t.section {
	case sqlSessions:
		l := t.sessions
		idle := "idle hidden"
		if t.showIdle {
			idle = "idle shown"
		}
		if k := t.keys.KeysFor(keymap.DBServer, keymap.ToggleIdle); k != "" {
			idle += " (" + k + ")"
		}
		s := plural(l.Total, "session") + " · " + strconv.Itoa(l.Active) + " running · " + idle
		if slices.Contains(l.Self, true) {
			s += " · * datavim"
		}
		return s
	case sqlSettings:
		if t.filter != "" {
			return "filter: " + t.filter
		}
	case sqlStatus, sqlLocks, sqlSlow, sqlReplication, sqlSections:
	}
	return ""
}

func (t *sqlServerTab) emptyText() string {
	switch t.section {
	case sqlSessions:
		return "no sessions"
	case sqlLocks:
		return "no lock waits"
	case sqlSlow:
		return "no statements recorded"
	case sqlSettings:
		if t.filter != "" {
			return "no match for " + t.filter
		}
		return "no settings"
	case sqlReplication:
		return "no replication configured"
	case sqlStatus, sqlSections:
	}
	return ""
}

func (t *sqlServerTab) statusLines() []string {
	var lines []string
	if t.statusErr != nil {
		lines = append(lines, errLine(t.statusErr))
	}
	if len(t.samples) == 0 {
		if t.statusErr == nil {
			lines = append(lines, theme.Muted.Render("loading…"))
		}
		return lines
	}
	last := t.samples[len(t.samples)-1]
	lines = append(lines, "")
	for _, f := range last.Facts {
		lines = append(lines, padRight(f.Label, 12)+textutil.Sanitize(formatFact(f)))
	}
	lines = append(lines, "")
	const labelW, valueW = 15, 12
	width := max(t.width-labelW-valueW, 0)
	for i, m := range last.Metrics {
		var vals []float64
		for k := range t.samples {
			var prev *db.ServerSample
			if k > 0 {
				prev = &t.samples[k-1]
			}
			if v := metricPoint(prev, t.samples[k], i); !math.IsNaN(v) {
				vals = append(vals, v)
			}
		}
		value := missing
		if len(vals) > 0 && !math.IsNaN(metricPoint(prevOf(t.samples), last, i)) {
			value = metricText(m, vals[len(vals)-1])
		}
		lines = append(lines, padRight(textutil.Sanitize(m.Name), labelW)+padRight(value, valueW)+
			theme.Accent.Render(sparkline.Render(vals, width, true)))
	}
	return lines
}

// prevOf is the sample before the last, or nil.
func prevOf(samples []db.ServerSample) *db.ServerSample {
	if len(samples) < 2 {
		return nil
	}
	return &samples[len(samples)-2]
}
