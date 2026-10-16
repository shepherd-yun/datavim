package ui

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
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
		return newServerTab(id, m.backend, m.keys, conn, m.timings.server)
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

	width, height int
}

func newServerTab(id int, b Backend, keys *keymap.Keymap, conn string, interval time.Duration) *serverTab {
	return &serverTab{id: id, backend: b, keys: keys, conn: conn, interval: interval}
}

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
	t.stopSampling()
	return t.startSampling()
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
	}
	return nil
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
	switch a {
	case keymap.NextSection:
		return t.setSection((t.section + 1) % serverSections)
	case keymap.PrevSection:
		return t.setSection((t.section + serverSections - 1) % serverSections)
	case keymap.Refresh:
		if t.section == secStatus {
			t.samples, t.statusErr = nil, ""
			t.stopSampling()
			return t.startSampling()
		}
	}
	return nil
}

func (t *serverTab) setSection(s serverSection) tea.Cmd {
	if s == t.section {
		return nil
	}
	t.section = s
	if s != secStatus {
		t.stopSampling()
		return nil
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
	if t.section == secStatus {
		lines = append(lines, t.statusLines()...)
	}
	for i, l := range lines {
		lines[i] = textutil.Fit(l, t.width)
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
