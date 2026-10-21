package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

func esServerTabKey(conn string) string { return "eserver\x00" + conn }

// openESServer opens (or shows) conn's cluster status tab.
func (m *Model) openESServer(conn string) tea.Cmd {
	return m.showTab(esServerTabKey(conn), func(id int) tab {
		return newESServerTab(id, m.backend, m.keys, m.settings.MaxColumnWidth, conn, m.timings.eserver)
	})
}

// esServerLoadedMsg is one reading of the cluster.
type esServerLoadedMsg struct {
	tabID, gen int
	conn       string
	info       es.Info
	health     es.Health
	stats      es.ClusterStats
	statsErr   string
	err        error
}

func (m esServerLoadedMsg) TabID() int               { return m.tabID }
func (m esServerLoadedMsg) esMsg()                   {}
func (m esServerLoadedMsg) failure() (string, error) { return m.conn, m.err }

type esServerTickMsg struct{ tabID, gen int }

func (m esServerTickMsg) TabID() int { return m.tabID }

// esServerTab is an Elasticsearch cluster's status (E1: the Overview),
// read every interval while on screen.
type esServerTab struct {
	id       int
	backend  Backend
	keys     *keymap.Keymap
	conn     string
	interval time.Duration
	grid     *grid.Model

	last    esServerLoadedMsg
	loaded  bool
	loading bool
	visible bool
	gen     int
	err     string

	width, height int
}

func newESServerTab(id int, b Backend, keys *keymap.Keymap, maxCol int, conn string, interval time.Duration) *esServerTab {
	return &esServerTab{id: id, backend: b, keys: keys, conn: conn, interval: interval, grid: grid.New(nil, maxCol), visible: true}
}

func (t *esServerTab) ID() int                       { return t.id }
func (t *esServerTab) Title() string                 { return t.conn + " · cluster" }
func (t *esServerTab) Key() string                   { return esServerTabKey(t.conn) }
func (t *esServerTab) Context() keymap.Context       { return keymap.ESServer }
func (t *esServerTab) Target() (string, db.TableRef) { return t.conn, db.TableRef{} }
func (t *esServerTab) SetSize(w, h int)              { t.width, t.height = max(w, 0), max(h, 0) }
func (t *esServerTab) Init() tea.Cmd                 { return t.load() }
func (t *esServerTab) retryAfterPassword() tea.Cmd   { return t.load() }
func (t *esServerTab) activeGrid() *grid.Model       { return t.grid }
func (t *esServerTab) Find(q string, fwd bool) bool  { return t.grid.Find(q, fwd) }

func (t *esServerTab) exportSource() exportSrc {
	return exportSrc{rs: t.grid.Result(), name: t.conn + "-cluster", conn: t.conn}
}

// Shown reads the cluster at once and goes on refreshing.
func (t *esServerTab) Shown() tea.Cmd {
	t.visible = true
	if t.loading {
		return nil
	}
	return t.load()
}

// Hidden stops the refreshes.
func (t *esServerTab) Hidden() {
	t.visible = false
	t.gen++
	t.loading = false
}

func (t *esServerTab) load() tea.Cmd {
	t.loading = true
	t.gen++
	id, gen, b, conn := t.id, t.gen, t.backend, t.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esServerLoadedMsg{tabID: id, gen: gen, conn: conn}
		out.err = withES(ctx, b, conn, func(d es.Doer) error {
			var err error
			if out.info, err = es.GetInfo(ctx, d); err != nil {
				return err
			}
			if out.health, err = es.ClusterHealth(ctx, d); err != nil {
				return err
			}
			if out.stats, err = es.GetClusterStats(ctx, d); err != nil {
				out.statsErr = esReason(err) // the rest still shows
			}
			return nil
		})
		return out
	}
}

func (t *esServerTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case esServerLoadedMsg:
		if m.gen != t.gen {
			return nil
		}
		t.loading = false
		if m.err != nil {
			t.err = esReason(m.err)
			return nil
		}
		t.err, t.loaded, t.last = "", true, m
		t.grid.ReplaceResult(t.overview())
		if !t.visible || t.interval <= 0 {
			return nil
		}
		id, gen := t.id, t.gen
		return tea.Tick(t.interval, func(time.Time) tea.Msg { return esServerTickMsg{tabID: id, gen: gen} })
	case esServerTickMsg:
		if m.gen == t.gen && t.visible && !t.loading {
			return t.load()
		}
	}
	return nil
}

func (t *esServerTab) Handle(a keymap.Action) tea.Cmd {
	if a == keymap.Refresh {
		if !t.loading {
			return t.load()
		}
		return nil
	}
	cmd, _ := gridAction(t.grid, a)
	return cmd
}

func (t *esServerTab) overview() *db.ResultSet {
	m := t.last
	h, s := m.health, m.stats
	n := func(v int) string { return groupDigits(int64(v)) }
	rows := [][]string{
		{"cluster", m.info.ClusterName},
		{"uuid", m.info.UUID},
		{"version", m.info.Version + " · " + m.info.Distribution},
		{"health", h.Status},
		{"nodes", fmt.Sprintf("%s (%s data)", n(h.Nodes), n(h.DataNodes))},
		{"shards", fmt.Sprintf("%s active · %s primary · %s relocating · %s initializing · %s unassigned",
			n(h.Active), n(h.ActivePrimary), n(h.Relocating), n(h.Initializing), n(h.Unassigned))},
		{"pending tasks", n(h.PendingTasks)},
	}
	if m.statsErr == "" {
		rows = append(rows,
			[]string{"indices", n(s.Indices)},
			[]string{"documents", groupDigits(s.Docs)},
			[]string{"size", humanBytes(s.StoreBytes)})
	} else {
		rows = append(rows, []string{"stats", m.statsErr})
	}
	return textResult([]string{"", "Overview"}, rows)
}

// warning is why the health is not green.
func (t *esServerTab) warning() string {
	h := t.last.health
	if h.Status == "green" || h.Status == "" {
		return ""
	}
	var why []string
	if h.Unassigned > 0 {
		why = append(why, fmt.Sprintf("%s unassigned shards", groupDigits(int64(h.Unassigned))))
	}
	if h.Initializing > 0 {
		why = append(why, fmt.Sprintf("%s initializing shards", groupDigits(int64(h.Initializing))))
	}
	if h.Relocating > 0 {
		why = append(why, fmt.Sprintf("%s relocating shards", groupDigits(int64(h.Relocating))))
	}
	return "cluster health " + h.Status + ": " + strings.Join(why, " · ")
}

func (t *esServerTab) View(focused bool) string {
	lines := []string{theme.Title.Render("Overview")}
	switch {
	case t.err != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.err)))
	case !t.loaded:
		lines = append(lines, theme.Muted.Render("loading…"))
	default:
		if w := t.warning(); w != "" {
			lines = append(lines, theme.Error.Render(textutil.Sanitize(w)))
		}
	}
	for i, l := range lines {
		lines[i] = textutil.Fit(l, t.width)
	}
	if t.loaded {
		t.grid.SetSize(t.width, max(t.height-len(lines), 0))
		lines = append(lines, t.grid.View(focused))
	}
	return strings.Join(lines, "\n")
}
