package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// Node warning thresholds of the Nodes section.
const (
	esHeapWarnPct      = 85
	esDiskWarnPct      = 90 // when the high watermark cannot be read
	esWatermarkSetting = "cluster.routing.allocation.disk.watermark.high"
)

type eserverSection int

const (
	secESOverview eserverSection = iota
	secESNodes
	secESShards
	secESAllocation
	secESTasks
	secESPending
	secESSettings
	eserverSections
)

var eserverSectionNames = [eserverSections]string{"Overview", "Nodes", "Shards", "Allocation", "Tasks", "Pending", "Settings"}

func esServerTabKey(conn string) string { return "eserver\x00" + conn }

// openESServer opens (or shows) conn's cluster status tab.
func (m *Model) openESServer(conn string) tea.Cmd {
	return m.showTab(esServerTabKey(conn), func(id int) tab {
		return newESServerTab(id, m.backend, m.keys, m.settings.MaxColumnWidth, conn, m.timings.eserver)
	})
}

// esServerLoadedMsg is one reading of the cluster: the health and the
// section on screen.
type esServerLoadedMsg struct {
	tabID, gen int
	conn       string
	section    eserverSection
	health     es.Health

	info     es.Info
	stats    es.ClusterStats
	statsErr string

	nodes     []es.Node
	alloc     []es.NodeAllocation
	watermark *string // read once; nil when not read this time
	shards    []es.Shard
	tasks     []es.Task
	pending   []es.PendingTask
	settings  []es.ClusterSetting
	err       error
}

func (m esServerLoadedMsg) TabID() int               { return m.tabID }
func (m esServerLoadedMsg) esMsg()                   {}
func (m esServerLoadedMsg) failure() (string, error) { return m.conn, m.err }

type esServerTickMsg struct{ tabID, gen int }

func (m esServerTickMsg) TabID() int { return m.tabID }

// esServerTab is an Elasticsearch cluster's status in sections (spec §7),
// the one on screen read every interval while the tab is visible.
type esServerTab struct {
	id       int
	backend  Backend
	keys     *keymap.Keymap
	conn     string
	interval time.Duration
	section  eserverSection
	grids    [eserverSections]*grid.Model
	loadedAt [eserverSections]bool

	health   es.Health
	info     es.Info
	stats    es.ClusterStats
	statsErr string
	nodes    []es.Node
	alloc    []es.NodeAllocation
	pending  []es.PendingTask
	shards   []es.Shard
	tasks    []es.Task
	// csettings are the cluster settings read, csView the Settings rows.
	csettings    []es.ClusterSetting
	csView       []es.ClusterSetting
	clusterEdits []clusterEdit
	showDefaults bool
	// shardView are the Shards rows shown: not STARTED unless shardsAll,
	// matching shardFilter.
	shardView   []es.Shard
	shardsAll   bool
	shardFilter string
	// watermark is the high disk watermark; read is whether it was tried.
	watermark     string
	watermarkRead bool

	loaded  bool
	loading bool
	visible bool
	gen     int
	err     string
	status  string
	statusE bool

	width, height int
}

func newESServerTab(id int, b Backend, keys *keymap.Keymap, maxCol int, conn string, interval time.Duration) *esServerTab {
	t := &esServerTab{id: id, backend: b, keys: keys, conn: conn, interval: interval, visible: true}
	for i := range t.grids {
		t.grids[i] = grid.New(nil, maxCol)
	}
	t.grids[secESNodes].SetMarks(t.nodeMark)
	t.grids[secESSettings].SetMarks(t.clusterMark)
	return t
}

func (t *esServerTab) ID() int                        { return t.id }
func (t *esServerTab) Title() string                  { return t.conn + " · cluster" }
func (t *esServerTab) Key() string                    { return esServerTabKey(t.conn) }
func (t *esServerTab) Target() (string, db.TableRef)  { return t.conn, db.TableRef{} }
func (t *esServerTab) SetSize(w, h int)               { t.width, t.height = max(w, 0), max(h, 0) }
func (t *esServerTab) Init() tea.Cmd                  { return t.load() }
func (t *esServerTab) retryAfterPassword() tea.Cmd    { return t.load() }
func (t *esServerTab) activeGrid() *grid.Model        { return t.grids[t.section] }
func (t *esServerTab) Find(q string, fwd bool) bool   { return t.activeGrid().Find(q, fwd) }
func (t *esServerTab) setStatus(s string, isErr bool) { t.status, t.statusE = s, isErr }

func (t *esServerTab) Context() keymap.Context {
	if t.section == secESSettings {
		return keymap.ESClusterSettings
	}
	return keymap.ESServer
}

func (t *esServerTab) exportSource() exportSrc {
	name := t.conn + "-" + strings.ToLower(eserverSectionNames[t.section])
	return exportSrc{rs: t.activeGrid().Result(), name: name, conn: t.conn}
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

// setSection shows section s and reads it at once.
func (t *esServerTab) setSection(s eserverSection) tea.Cmd {
	t.section = s
	return t.load()
}

func (t *esServerTab) load() tea.Cmd {
	t.loading = true
	t.gen++
	id, gen, b, conn, sec := t.id, t.gen, t.backend, t.conn, t.section
	readWatermark := sec == secESNodes && !t.watermarkRead
	shardIndex := ""
	if f := t.shardFilter; f != "" && !strings.ContainsAny(f, "*?,") {
		shardIndex = f // a concrete index: read only its shards
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esServerLoadedMsg{tabID: id, gen: gen, conn: conn, section: sec}
		out.err = withES(ctx, b, conn, func(d es.Doer) error {
			var err error
			if out.health, err = es.ClusterHealth(ctx, d); err != nil {
				return err
			}
			switch sec {
			case secESOverview:
				if out.info, err = es.GetInfo(ctx, d); err != nil {
					return err
				}
				if out.stats, err = es.GetClusterStats(ctx, d); err != nil {
					out.statsErr = esReason(err) // the rest still shows
				}
			case secESNodes:
				if out.nodes, err = es.Nodes(ctx, d); err != nil {
					return err
				}
				if out.alloc, err = es.Allocation(ctx, d); err != nil {
					return err
				}
				if readWatermark {
					w := ""
					if set, err := es.ClusterSettings(ctx, d); err == nil {
						w = clusterSettingValue(set, esWatermarkSetting)
					}
					out.watermark = &w
				}
			case secESShards:
				out.shards, err = es.Shards(ctx, d, shardIndex)
			case secESAllocation:
				out.alloc, err = es.Allocation(ctx, d)
			case secESTasks:
				out.tasks, err = es.Tasks(ctx, d)
			case secESPending:
				out.pending, err = es.PendingTasks(ctx, d)
			case secESSettings:
				out.settings, err = es.ClusterSettings(ctx, d)
			}
			return err
		})
		return out
	}
}

// clusterSettingValue is a setting's effective value: transient over
// persistent over the default.
func clusterSettingValue(set []es.ClusterSetting, key string) string {
	for _, scope := range []string{"transient", "persistent", "defaults"} {
		for _, s := range set {
			if s.Key == key && s.Scope == scope {
				return s.Value
			}
		}
	}
	return ""
}

func (t *esServerTab) Update(msg tea.Msg) tea.Cmd {
	if cmd, ok := t.clusterUpdate(msg); ok {
		return cmd
	}
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
		t.err, t.loaded = "", true
		t.apply(m)
		if !t.visible || t.interval <= 0 {
			return nil
		}
		id, gen := t.id, t.gen
		return tea.Tick(t.interval, func(time.Time) tea.Msg { return esServerTickMsg{tabID: id, gen: gen} })
	case esServerTickMsg:
		if m.gen == t.gen && t.visible && !t.loading {
			return t.load()
		}
	case esServerDetailMsg:
		if m.err != nil {
			t.setStatus(esReason(m.err), true)
			return nil
		}
		return openOverlay(overlay.NewDetail(m.title, m.text))
	case esCancelTaskMsg:
		return t.runCancel(m.id)
	case esTaskCancelledMsg:
		return t.taskCancelled(m)
	case esShardFilterMsg:
		t.shardFilter = m.text
		return t.setSection(secESShards)
	}
	return nil
}

func (t *esServerTab) apply(m esServerLoadedMsg) {
	t.health = m.health
	t.loadedAt[m.section] = true
	switch m.section {
	case secESOverview:
		t.info, t.stats, t.statsErr = m.info, m.stats, m.statsErr
		t.grids[secESOverview].ReplaceResult(t.overview())
	case secESNodes:
		if m.watermark != nil {
			t.watermark, t.watermarkRead = *m.watermark, true
		}
		t.nodes, t.alloc = m.nodes, m.alloc
		t.grids[secESNodes].ReplaceResult(t.nodesResult())
	case secESShards:
		t.shards = m.shards
		t.renderShards()
	case secESSettings:
		t.csettings = m.settings
		t.renderClusterSettings()
	case secESTasks:
		t.tasks = m.tasks
		t.grids[secESTasks].ReplaceResult(t.tasksResult())
	case secESAllocation:
		t.alloc = m.alloc
		t.grids[secESAllocation].ReplaceResult(t.allocationResult())
	case secESPending:
		t.pending = m.pending
		t.grids[secESPending].ReplaceResult(t.pendingResult())
	}
}

func (t *esServerTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	if t.section == secESShards {
		if cmd, ok := t.handleShards(a); ok {
			return cmd
		}
	}
	if t.section == secESSettings {
		if cmd, ok := t.handleClusterSettings(a); ok {
			return cmd
		}
	}
	switch a {
	case keymap.NextSection:
		return t.setSection(min(t.section+1, eserverSections-1))
	case keymap.PrevSection:
		return t.setSection(max(t.section-1, 0))
	case keymap.Refresh:
		if !t.loading {
			return t.load()
		}
		return nil
	case keymap.Open:
		switch t.section {
		case secESOverview:
			if t.health.Status != "green" && t.health.Status != "" {
				return t.setSection(secESShards)
			}
		case secESNodes:
			if r, _ := t.grids[secESNodes].Position(); r < len(t.nodes) {
				return t.nodeDetail(t.nodes[r])
			}
		}
		return nil
	case keymap.KillClient:
		if t.section == secESTasks {
			return t.cancelTask()
		}
		return nil
	}
	cmd, _ := gridAction(t.activeGrid(), a)
	return cmd
}

func (t *esServerTab) overview() *db.ResultSet {
	h, s := t.health, t.stats
	n := func(v int) string { return groupDigits(int64(v)) }
	rows := [][]string{
		{"cluster", t.info.ClusterName},
		{"uuid", t.info.UUID},
		{"version", t.info.Version + " · " + t.info.Distribution},
		{"health", h.Status},
		{"nodes", fmt.Sprintf("%s (%s data)", n(h.Nodes), n(h.DataNodes))},
		{"shards", fmt.Sprintf("%s active · %s primary · %s relocating · %s initializing · %s unassigned",
			n(h.Active), n(h.ActivePrimary), n(h.Relocating), n(h.Initializing), n(h.Unassigned))},
		{"pending tasks", n(h.PendingTasks)},
	}
	if t.statsErr == "" {
		rows = append(rows,
			[]string{"indices", n(s.Indices)},
			[]string{"documents", groupDigits(s.Docs)},
			[]string{"size", humanBytes(s.StoreBytes)})
	} else {
		rows = append(rows, []string{"stats", t.statsErr})
	}
	return textResult([]string{"", "Overview"}, rows)
}

// pct is a percentage cell, empty when not reported (< 0).
func pct(v float64) string {
	if v < 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// bytesCell is a byte size cell, empty when not reported (< 0).
func bytesCell(v int64) string {
	if v < 0 {
		return ""
	}
	return humanBytes(v)
}

func (t *esServerTab) nodesResult() *db.ResultSet {
	shards := map[string]int{}
	for _, a := range t.alloc {
		shards[a.Node] = a.Shards
	}
	rows := make([][]string, len(t.nodes))
	for i, n := range t.nodes {
		master, load := "", ""
		if n.Master {
			master = "★"
		}
		if n.Load1m >= 0 {
			load = strconv.FormatFloat(n.Load1m, 'f', 2, 64)
		}
		rows[i] = []string{
			n.Name, n.IP, n.Roles, master, strconv.Itoa(n.HeapPct), strconv.Itoa(n.RAMPct), strconv.Itoa(n.CPUPct),
			load, pct(n.DiskUsedPct), groupDigits(int64(shards[n.Name])), n.Version,
		}
	}
	return textResult([]string{"NAME", "IP", "ROLES", "MASTER", "HEAP %", "RAM %", "CPU %", "LOAD 1M", "DISK %", "SHARDS", "VERSION"}, rows)
}

// nodeWarn reports a node short of heap or past the disk watermark.
func (t *esServerTab) nodeWarn(n es.Node) bool {
	if n.HeapPct >= esHeapWarnPct {
		return true
	}
	if n.DiskUsedPct < 0 {
		return false
	}
	limit := float64(esDiskWarnPct)
	if w, ok := esx.Watermark(t.watermark, n.DiskTotal); ok {
		limit = w
	}
	return n.DiskUsedPct >= limit
}

// nodeMark marks the nodes in trouble.
func (t *esServerTab) nodeMark(row, _ int) grid.Mark {
	if row < len(t.nodes) && t.nodeWarn(t.nodes[row]) {
		return grid.MarkError
	}
	return grid.MarkNone
}

// renderShards fills the Shards grid from the last reading.
func (t *esServerTab) renderShards() {
	t.shardView = t.shardView[:0]
	for _, s := range t.shards {
		if (t.shardsAll || s.State != "STARTED") && (t.shardFilter == "" || indexNameMatch(t.shardFilter, s.Index)) {
			t.shardView = append(t.shardView, s)
		}
	}
	rows := make([][]string, len(t.shardView))
	for i, s := range t.shardView {
		pr := "r"
		if s.Primary {
			pr = "p"
		}
		docs := ""
		if s.Docs >= 0 {
			docs = groupDigits(s.Docs)
		}
		rows[i] = []string{s.Index, strconv.Itoa(s.Shard), pr, s.State, s.Node, docs, bytesCell(s.Bytes), s.Reason}
	}
	t.grids[secESShards].ReplaceResult(textResult([]string{"INDEX", "SHARD", "P/R", "STATE", "NODE", "DOCS", "SIZE", "REASON"}, rows))
}

func (t *esServerTab) allocationResult() *db.ResultSet {
	rows := make([][]string, len(t.alloc))
	for i, a := range t.alloc {
		disk := ""
		if a.DiskPct >= 0 {
			disk = strconv.Itoa(a.DiskPct)
		}
		rows[i] = []string{a.Node, groupDigits(int64(a.Shards)), bytesCell(a.DiskIndices), bytesCell(a.DiskUsed), bytesCell(a.DiskAvail), bytesCell(a.DiskTotal), disk}
	}
	return textResult([]string{"NODE", "SHARDS", "DISK INDICES", "DISK USED", "DISK AVAIL", "DISK TOTAL", "DISK %"}, rows)
}

func (t *esServerTab) pendingResult() *db.ResultSet {
	rows := make([][]string, len(t.pending))
	for i, p := range t.pending {
		rows[i] = []string{strconv.FormatInt(p.Order, 10), p.Priority, p.Source, p.Waiting}
	}
	return textResult([]string{"ORDER", "PRIORITY", "SOURCE", "WAITING"}, rows)
}

// warning is why the health is not green.
func (t *esServerTab) warning() string {
	h := t.health
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

// empty is what a loaded section with no rows says instead.
func (t *esServerTab) empty() string {
	switch {
	case t.section == secESPending && len(t.pending) == 0:
		return "no pending cluster tasks"
	case t.section == secESTasks && len(t.tasks) == 0:
		return "no long-running tasks"
	case t.section == secESShards && len(t.shardView) == 0 && !t.shardsAll && t.shardFilter == "":
		return "every shard is started"
	}
	return ""
}

// sectionNote is shown under the section titles.
func (t *esServerTab) sectionNote() string {
	switch {
	case t.section == secESSettings && t.showDefaults:
		return "defaults shown"
	case t.section != secESShards:
		return ""
	}
	var n []string
	if t.shardsAll {
		n = append(n, "all shards")
	}
	if t.shardFilter != "" {
		n = append(n, "filter "+t.shardFilter)
	}
	return strings.Join(n, " · ")
}

func (t *esServerTab) View(focused bool) string {
	var lines []string
	if w := t.warning(); w != "" && t.loaded {
		lines = append(lines, theme.Error.Render(textutil.Sanitize(w)))
	}
	names := make([]string, eserverSections)
	for i, n := range eserverSectionNames {
		if eserverSection(i) == t.section {
			names[i] = theme.Title.Render(n)
		} else {
			names[i] = theme.Muted.Render(n)
		}
	}
	lines = append(lines, strings.Join(names, theme.Muted.Render(" · ")))
	if note := t.sectionNote(); note != "" {
		lines = append(lines, theme.Muted.Render(textutil.Sanitize(note)))
	}
	if t.status != "" {
		st := theme.Muted
		if t.statusE {
			st = theme.Error
		}
		lines = append(lines, st.Render(textutil.Sanitize(t.status)))
	}
	var g *grid.Model
	switch {
	case t.err != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.err)))
	case !t.loadedAt[t.section]:
		lines = append(lines, theme.Muted.Render("loading…"))
	case t.empty() != "":
		lines = append(lines, theme.Muted.Render(t.empty()))
	default:
		g = t.activeGrid()
	}
	for i, l := range lines {
		lines[i] = textutil.Fit(l, t.width)
	}
	if g != nil {
		g.SetSize(t.width, max(t.height-len(lines), 0))
		lines = append(lines, g.View(focused))
	}
	return strings.Join(lines, "\n")
}
