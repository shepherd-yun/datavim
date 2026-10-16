package ui

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/rcmd"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/sparkline"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// serverSection is one part of the server tab.
type serverSection int

const (
	secStatus serverSection = iota
	secSlowLog
	secClients
	serverSections
)

var sectionNames = [serverSections]string{"Status", "Slow log", "Clients"}

// slowLogCount is how many slow log entries the slow log section reads.
const slowLogCount = 128

// shower is a tab that does work only while it is on screen.
type shower interface {
	Shown() tea.Cmd
	Hidden()
}

// syncShown tells the tab that was on screen it is hidden and the one now
// on screen it is shown, when the active tab changed.
func (m *Model) syncShown() tea.Cmd {
	cur := 0
	if t := m.activeTab(); t != nil {
		cur = t.ID()
	}
	if cur == m.shownTab {
		return nil
	}
	if old, ok := m.tabByID(m.shownTab).(shower); ok {
		old.Hidden()
	}
	m.shownTab = cur
	if s, ok := m.activeTab().(shower); ok {
		return s.Shown()
	}
	return nil
}

func serverTabKey(conn string) string { return "rserver\x00" + conn }

// openServer opens (or shows) the server tab of conn.
func (m *Model) openServer(conn string) tea.Cmd {
	return m.showTab(serverTabKey(conn), func(id int) tab {
		return newServerTab(id, m.backend, m.keys, conn, m.timings.server, m.settings)
	})
}

type serverTickMsg struct{ tabID, gen int }

func (m serverTickMsg) TabID() int { return m.tabID }

type serverInfoMsg struct {
	tabID, gen int
	conn       string
	info       rdb.Info
	err        error
}

func (m serverInfoMsg) TabID() int               { return m.tabID }
func (m serverInfoMsg) failure() (string, error) { return m.conn, m.err }

// serverLoadedMsg is the slow log or the clients; ids are the clients'
// IDs, row by row.
type serverLoadedMsg struct {
	tabID int
	conn  string
	sec   serverSection
	rs    *db.ResultSet
	ids   []int64
	err   error
}

func (m serverLoadedMsg) TabID() int               { return m.tabID }
func (m serverLoadedMsg) failure() (string, error) { return m.conn, m.err }

// killConfirmedMsg is a confirmed CLIENT KILL.
type killConfirmedMsg struct {
	tabID int
	id    int64
	addr  string
}

func (m killConfirmedMsg) TabID() int { return m.tabID }

type clientKilledMsg struct {
	tabID int
	conn  string
	id    int64
	addr  string
	ok    bool
	err   error
}

func (m clientKilledMsg) TabID() int               { return m.tabID }
func (m clientKilledMsg) failure() (string, error) { return m.conn, m.err }

// serverTab is a redis connection's server tab: the status (sampled every
// statusInterval while it is on screen), the slow log and the clients.
type serverTab struct {
	id       int
	backend  Backend
	keys     *keymap.Keymap
	conn     string
	interval time.Duration
	section  serverSection

	visible bool
	running bool
	gen     int // tells the current sampling from stopped ones

	info      rdb.Info
	samples   []statusSample
	statusErr string

	// the slow log and clients sections share the grid
	grid    *grid.Model
	gridSec serverSection // the section the grid holds; serverSections: none
	ids     []int64       // the clients' IDs, row by row
	loading bool
	secErr  string
	status  string
	statusE bool

	width, height int
}

func newServerTab(id int, b Backend, keys *keymap.Keymap, conn string, interval time.Duration, s config.Settings) *serverTab {
	return &serverTab{
		id: id, backend: b, keys: keys, conn: conn, interval: interval,
		grid: grid.New(nil, s.MaxColumnWidth), gridSec: serverSections,
	}
}

func (t *serverTab) activeGrid() *grid.Model { return t.grid }

func (t *serverTab) exportSource() exportSrc {
	if t.section == secStatus || t.gridSec != t.section || t.grid.Result() == nil {
		return exportSrc{}
	}
	name := map[serverSection]string{secSlowLog: "slowlog", secClients: "clients"}[t.section]
	return exportSrc{rs: t.grid.Result(), name: name, conn: t.conn}
}

func (t *serverTab) Find(q string, forward bool) bool {
	return t.section != secStatus && t.grid.Find(q, forward)
}

func (t *serverTab) setStatus(s string, isErr bool) { t.status, t.statusE = s, isErr }

func (t *serverTab) ID() int                 { return t.id }
func (t *serverTab) Title() string           { return t.conn + " · server" }
func (t *serverTab) Key() string             { return serverTabKey(t.conn) }
func (t *serverTab) Context() keymap.Context { return keymap.RedisServer }
func (t *serverTab) Init() tea.Cmd           { return nil } // syncShown starts the sampling
func (t *serverTab) SetSize(w, h int)        { t.width, t.height = max(w, 0), max(h, 0) }

func (t *serverTab) Target() (string, db.TableRef) { return t.conn, db.TableRef{} }

func (t *serverTab) Shown() tea.Cmd {
	t.visible = true
	return t.startSampling()
}

func (t *serverTab) Hidden() {
	t.visible = false
	t.stopSampling()
}

func (t *serverTab) retryAfterPassword() tea.Cmd {
	if t.section != secStatus {
		return t.load()
	}
	t.stopSampling()
	return t.startSampling()
}

// load reads the slow log or the clients, whichever section is current.
func (t *serverTab) load() tea.Cmd {
	t.loading = true
	id, b, conn, sec := t.id, t.backend, t.conn, t.section
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := serverLoadedMsg{tabID: id, conn: conn, sec: sec}
		msg.err = withRedis(ctx, b, conn, -1, func(c rdb.Client) error {
			switch sec {
			case secSlowLog:
				es, err := c.SlowLog(ctx, slowLogCount)
				if err != nil {
					return err
				}
				msg.rs = slowLogResult(es)
			case secClients:
				cs, err := c.Clients(ctx)
				if err != nil {
					return err
				}
				msg.rs, msg.ids = clientsResult(cs)
			}
			return nil
		})
		return msg
	}
}

func textResult(cols []string, rows [][]string) *db.ResultSet {
	rs := &db.ResultSet{Columns: make([]db.ColumnInfo, len(cols)), Rows: make([][]db.Value, len(rows))}
	for i, c := range cols {
		rs.Columns[i] = db.ColumnInfo{Name: c}
	}
	for i, r := range rows {
		rs.Rows[i] = make([]db.Value, len(r))
		for j, v := range r {
			rs.Rows[i][j] = db.Value{Text: v}
		}
	}
	return rs
}

// slowLogResult is the slow log as a table; the commands are redacted
// (and quoted, so binary arguments show as \xNN).
func slowLogResult(es []rdb.SlowEntry) *db.ResultSet {
	rows := make([][]string, len(es))
	for i, e := range es {
		rows[i] = []string{
			strconv.FormatInt(e.ID, 10), e.Time.Local().Format(time.DateTime), durationText(e.Duration),
			rcmd.Redact(e.Args), e.Addr, e.Name,
		}
	}
	return textResult([]string{"id", "time", "duration", "command", "client", "name"}, rows)
}

// durationText is 850 µs, 12.3 ms or 1.24 s.
func durationText(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return strconv.FormatInt(d.Microseconds(), 10) + " µs"
	case d < time.Second:
		return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 1, 64) + " ms"
	}
	return strconv.FormatFloat(d.Seconds(), 'f', 2, 64) + " s"
}

func clientsResult(cs []rdb.ClientInfo) (*db.ResultSet, []int64) {
	cols := []string{"id", "addr", "name", "age", "idle", "db", "cmd", "flags"}
	rows := make([][]string, len(cs))
	ids := make([]int64, len(cs))
	for i, c := range cs {
		ids[i], _ = strconv.ParseInt(c["id"], 10, 64)
		row := make([]string, len(cols))
		for j, col := range cols {
			row[j] = c[col]
			if col == "age" || col == "idle" {
				if n, err := strconv.ParseInt(c[col], 10, 64); err == nil {
					row[j] = uptime(n)
				}
			}
		}
		rows[i] = row
	}
	return textResult(cols, rows), ids
}

// startSampling sends the first INFO when the status section is on screen
// and not already sampling.
func (t *serverTab) startSampling() tea.Cmd {
	if !t.visible || t.section != secStatus || t.running {
		return nil
	}
	t.gen++
	t.running = true
	return t.sample()
}

// stopSampling drops the pending tick and INFO: they carry an old gen.
func (t *serverTab) stopSampling() {
	if t.running {
		t.gen++
		t.running = false
	}
}

func (t *serverTab) sample() tea.Cmd {
	id, gen, b, conn := t.id, t.gen, t.backend, t.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := serverInfoMsg{tabID: id, gen: gen, conn: conn}
		msg.err = withRedis(ctx, b, conn, -1, func(c rdb.Client) error {
			var err error
			msg.info, err = c.Info(ctx)
			return err
		})
		return msg
	}
}

func (t *serverTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case serverInfoMsg:
		if m.gen != t.gen || !t.running {
			return nil
		}
		if m.err != nil {
			t.stopSampling()
			t.statusErr = serverErr(m.err)
			return nil
		}
		t.statusErr = ""
		t.info = m.info
		t.samples = append(t.samples, sampleOf(m.info))
		if over := len(t.samples) - statusSamples; over > 0 {
			t.samples = t.samples[over:]
		}
		id, gen := t.id, t.gen
		return tea.Tick(t.interval, func(time.Time) tea.Msg { return serverTickMsg{tabID: id, gen: gen} })
	case serverTickMsg:
		if m.gen == t.gen && t.running {
			return t.sample()
		}
	case serverLoadedMsg:
		if m.sec != t.section {
			return nil
		}
		t.loading = false
		if m.err != nil {
			t.secErr = serverErr(m.err)
			t.grid.SetResult(nil)
			t.gridSec, t.ids = serverSections, nil
			return nil
		}
		t.secErr = ""
		if t.gridSec == m.sec {
			t.grid.ReplaceResult(m.rs)
		} else {
			t.grid.SetResult(m.rs)
		}
		t.gridSec, t.ids = m.sec, m.ids
	case killConfirmedMsg:
		return t.kill(m.id, m.addr)
	case clientKilledMsg:
		switch {
		case m.err != nil:
			t.setStatus(serverErr(m.err), true)
			return nil
		case m.ok:
			t.setStatus(fmt.Sprintf("killed client %d (%s)", m.id, m.addr), false)
		default:
			t.setStatus(fmt.Sprintf("client %d is already gone", m.id), false)
		}
		if t.section == secClients {
			return t.load()
		}
	}
	return nil
}

// askKill checks CLIENT KILL ID for the client under the cursor: refused
// on read-only, confirmed everywhere (it is dangerous), with the
// connection's name typed on prod.
func (t *serverTab) askKill() tea.Cmd {
	if t.section != secClients {
		t.setStatus("kill works in the clients list", true)
		return nil
	}
	r, _ := t.grid.Position()
	rs := t.grid.Result()
	if t.gridSec != secClients || rs == nil || r >= len(t.ids) {
		t.setStatus("no client selected", true)
		return nil
	}
	id, addr := t.ids[r], rs.Rows[r][1].Text
	cfg, _ := t.backend.Connection(t.conn)
	policy := rcmd.Policy{ReadOnly: cfg.IsReadOnly(), Prod: cfg.Env == config.EnvProd, Conn: t.conn, DBSize: -1}
	chk := rcmd.GuardArgs([][]string{{"CLIENT", "KILL", "ID", strconv.FormatInt(id, 10)}}, policy)
	switch chk.Verdict {
	case rcmd.Reject:
		t.setStatus(chk.Reason, true)
		return nil
	case rcmd.Confirm:
		return openOverlay(overlay.NewConfirm(chk.Title, chk.Body, chk.Expect, killConfirmedMsg{tabID: t.id, id: id, addr: addr}))
	}
	return t.kill(id, addr)
}

func (t *serverTab) kill(id int64, addr string) tea.Cmd {
	tabID, b, conn := t.id, t.backend, t.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := clientKilledMsg{tabID: tabID, conn: conn, id: id, addr: addr}
		msg.err = withRedis(ctx, b, conn, -1, func(c rdb.Client) error {
			var err error
			msg.ok, err = c.KillClient(ctx, id)
			return err
		})
		return msg
	}
}

// serverErr is how a section shows err: an ACL refusal is "permission
// denied", anything else the server's words.
func serverErr(err error) string {
	if rdb.IsNoPerm(err) {
		return "permission denied"
	}
	return err.Error()
}

func (t *serverTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	switch a {
	case keymap.NextSection:
		return t.setSection((t.section + 1) % serverSections)
	case keymap.PrevSection:
		return t.setSection((t.section + serverSections - 1) % serverSections)
	case keymap.Refresh:
		switch {
		case t.section == secStatus:
			t.samples, t.statusErr = nil, ""
			t.stopSampling()
			return t.startSampling()
		case !t.loading:
			return t.load()
		}
		return nil
	case keymap.KillClient:
		return t.askKill()
	}
	if t.section != secStatus {
		if cmd, ok := gridAction(t.grid, a); ok {
			return cmd
		}
	}
	return nil
}

func (t *serverTab) setSection(s serverSection) tea.Cmd {
	if s == t.section {
		return nil
	}
	t.section = s
	t.grid.SetResult(nil)
	t.gridSec, t.ids, t.secErr = serverSections, nil, ""
	if s != secStatus {
		t.stopSampling()
		return t.load()
	}
	return t.startSampling()
}

func (t *serverTab) View(focused bool) string {
	names := make([]string, serverSections)
	for i, n := range sectionNames {
		if serverSection(i) == t.section {
			names[i] = theme.Title.Render(n)
		} else {
			names[i] = theme.Muted.Render(n)
		}
	}
	lines := []string{strings.Join(names, theme.Muted.Render(" · "))}
	if t.status != "" {
		st := theme.Muted
		if t.statusE {
			st = theme.Error
		}
		lines = append(lines, st.Render(textutil.Sanitize(t.status)))
	}
	body := ""
	switch {
	case t.section == secStatus:
		lines = append(lines, t.statusLines()...)
	case t.secErr != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.secErr)))
	case t.gridSec != t.section:
		lines = append(lines, theme.Muted.Render("loading…"))
	case t.grid.RowCount() == 0 && t.section == secSlowLog:
		lines = append(lines, theme.Muted.Render("the slow log is empty"))
	case t.grid.RowCount() == 0:
		lines = append(lines, theme.Muted.Render("no clients"))
	default:
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

func (t *serverTab) statusLines() []string {
	var lines []string
	if t.statusErr != "" {
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.statusErr)))
	}
	if t.info == nil {
		if t.statusErr == "" {
			lines = append(lines, theme.Muted.Render("loading…"))
		}
		return lines
	}
	lines = append(lines, "")
	for _, f := range statusFacts(t.info) {
		lines = append(lines, padRight(f.label, 10)+textutil.Sanitize(f.value))
	}
	lines = append(lines, "")
	series := func(get func(statusSample) float64) []float64 {
		var out []float64
		for _, s := range t.samples {
			if v := get(s); !math.IsNaN(v) {
				out = append(out, v)
			}
		}
		return out
	}
	const labelW, valueW = 9, 12
	width := max(t.width-labelW-valueW, 0)
	spark := func(label string, get func(statusSample) float64, format func(float64) string, fromZero bool) {
		value := missing
		if n := len(t.samples); n > 0 && !math.IsNaN(get(t.samples[n-1])) {
			value = format(get(t.samples[n-1]))
		}
		vals := series(get)
		lines = append(lines, padRight(label, labelW)+padRight(value, valueW)+theme.Accent.Render(sparkline.Render(vals, width, fromZero)))
	}
	kbps := func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) + " KB/s" }
	spark("ops/sec", func(s statusSample) float64 { return s.ops }, func(v float64) string { return groupDigits(int64(v)) }, true)
	spark("memory", func(s statusSample) float64 { return s.mem }, func(v float64) string { return humanBytes(int64(v)) }, false)
	spark("net in", func(s statusSample) float64 { return s.inKbps }, kbps, true)
	spark("net out", func(s statusSample) float64 { return s.outKbps }, kbps, true)
	return lines
}

// padRight pads s with spaces to n columns (cells, not bytes).
func padRight(s string, n int) string {
	if w := ansi.StringWidth(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s + " "
}
