package ui

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/sparkline"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
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

	info db.ServerInfo

	visible bool
	running bool
	gen     int // tells the current sampling from stopped ones

	samples   []db.ServerSample
	statusErr error

	grid    *grid.Model
	gridSec sqlSection // the section the grid holds; sqlSections: none
	secErr  error
	status  string
	statusE bool

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
	t.stopSampling()
	return tea.Batch(t.loadInfo(), t.startSampling())
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
		if m.err == nil {
			t.info = m.info
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
		return nil
	}
	if t.section != sqlStatus {
		if cmd, ok := gridAction(t.grid, a); ok {
			return cmd
		}
	}
	return nil
}

func (t *sqlServerTab) setSection(s sqlSection) tea.Cmd {
	if s == t.section {
		return nil
	}
	t.section = s
	t.grid.SetResult(nil)
	t.gridSec, t.secErr = sqlSections, nil
	if s != sqlStatus {
		t.stopSampling()
		return nil
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

func (t *sqlServerTab) View(_ bool) string {
	lines := []string{sectionBar(t.sectionNames(), int(t.section), t.width)}
	if t.status != "" {
		st := theme.Muted
		if t.statusE {
			st = theme.Error
		}
		lines = append(lines, st.Render(textutil.Sanitize(t.status)))
	}
	if t.section == sqlStatus {
		lines = append(lines, t.statusLines()...)
	}
	for i, l := range lines {
		lines[i] = textutil.Fit(l, t.width)
	}
	return strings.Join(lines, "\n")
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
