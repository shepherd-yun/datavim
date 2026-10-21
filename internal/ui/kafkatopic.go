package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// topicSection is one part of a topic tab.
type topicSection int

const (
	secMessages topicSection = iota
	secPartitions
	secConfig
	secConsumers
	topicSections
)

var topicSectionNames = [topicSections]string{"Messages", "Partitions", "Config", "Consumers"}

func topicTabKey(conn, topic string) string { return "ktopic\x00" + conn + "\x00" + topic }

// kafkaMsg marks the async results of the kafka tabs: a missing password
// asks for one and the tab tries again.
type kafkaMsg interface {
	tabMsg
	kafkaMsg()
}

// openGroupMsg asks the root model to open a group's tab.
type openGroupMsg struct{ conn, group string }

func openGroupCmd(conn, group string) tea.Cmd {
	return func() tea.Msg { return openGroupMsg{conn: conn, group: group} }
}

// topicLoadedMsg is a topic's partitions and configs.
type topicLoadedMsg struct {
	tabID   int
	conn    string
	detail  kafka.TopicDetail
	configs []kafka.ConfigEntry
	cfgErr  error // the configs alone could not be read
	err     error
}

func (m topicLoadedMsg) TabID() int               { return m.tabID }
func (m topicLoadedMsg) kafkaMsg()                {}
func (m topicLoadedMsg) failure() (string, error) { return m.conn, m.err }

// consumersLoadedMsg is the groups that consume a topic.
type consumersLoadedMsg struct {
	tabID  int
	conn   string
	groups []kafka.GroupDetail
	err    error
}

func (m consumersLoadedMsg) TabID() int               { return m.tabID }
func (m consumersLoadedMsg) kafkaMsg()                {}
func (m consumersLoadedMsg) failure() (string, error) { return m.conn, m.err }

// topicTab is a Kafka topic: its messages, partitions, configs and the
// groups that consume it.
type topicTab struct {
	id       int
	backend  Backend
	keys     *keymap.Keymap
	settings config.Settings
	conn     string
	topic    string
	section  topicSection

	detail    *kafka.TopicDetail
	configs   []kafka.ConfigEntry
	cfgEdits  []kmsgx.ConfigEdit // staged in the Config section, in order
	detailErr string
	cfgErr    string
	loading   bool

	msgGrid                     *grid.Model
	msgs                        *msgView
	partGrid, cfgGrid, consGrid *grid.Model
	consumers                   []kafka.GroupDetail
	consLoaded, consLoading     bool
	consErr                     string

	status  string
	statusE bool

	width, height int

	ctx    context.Context // every load's; shutdown cancels it
	cancel context.CancelFunc
}

func newTopicTab(id int, b Backend, keys *keymap.Keymap, s config.Settings, conn, topic string) *topicTab {
	t := &topicTab{
		id: id, backend: b, keys: keys, settings: s, conn: conn, topic: topic,
		msgGrid:  grid.New(nil, s.MaxColumnWidth),
		partGrid: grid.New(nil, s.MaxColumnWidth), cfgGrid: grid.New(nil, s.MaxColumnWidth), consGrid: grid.New(nil, s.MaxColumnWidth),
	}
	t.msgs = &msgView{grid: t.msgGrid, pos: kmsgx.Position{Kind: kmsgx.PosLatest}, limit: msgLimit}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	t.partGrid.SetMarks(t.partMark)
	t.cfgGrid.SetMarks(t.cfgMark)
	return t
}

// openTopic opens (or shows) the tab of topic on conn.
func (m *Model) openTopic(conn, topic string) tea.Cmd {
	rec := history.Recent{Kind: history.RecentKafkaTopic, Conn: conn, Name: topic}
	return m.openTab(topicTabKey(conn, topic), rec, func(id int) tab {
		return newTopicTab(id, m.backend, m.keys, m.settings, conn, topic)
	})
}

func (t *topicTab) ID() int       { return t.id }
func (t *topicTab) Title() string { return t.topic }
func (t *topicTab) Key() string   { return topicTabKey(t.conn, t.topic) }
func (t *topicTab) Context() keymap.Context {
	switch t.section {
	case secMessages:
		return keymap.KafkaMessages
	case secConfig:
		return keymap.KafkaConfig
	}
	return keymap.KafkaTopic
}
func (t *topicTab) Target() (string, db.TableRef) { return t.conn, db.TableRef{} }
func (t *topicTab) SetSize(w, h int)              { t.width, t.height = max(w, 0), max(h, 0) }
func (t *topicTab) Init() tea.Cmd {
	return tea.Batch(t.loadDetail(), t.loadMsgs(0, t.msgs.pos, nil))
}

// shutdown cancels the loads and closes the message reader when the tab
// closes.
func (t *topicTab) shutdown() func() {
	t.cancel()
	r := t.msgs.reader
	t.msgs.reader = nil
	t.msgs.gen++
	return func() {
		if r != nil {
			_ = r.Close()
		}
	}
}

// orphanReader is the reader a load opened for a topic tab closed since.
func orphanReader(msg tea.Msg) kafka.Reader {
	switch m := msg.(type) {
	case msgsLoadedMsg:
		return m.reader
	case tailStartedMsg:
		return m.reader
	}
	return nil
}

func (t *topicTab) exportSource() exportSrc {
	if t.section == secMessages {
		return t.msgsExport()
	}
	g := t.activeGrid()
	return exportSrc{rs: g.Result(), name: t.topic + "-" + strings.ToLower(topicSectionNames[t.section]), conn: t.conn}
}

func (t *topicTab) setStatus(s string, isErr bool) { t.status, t.statusE = s, isErr }

func (t *topicTab) retryAfterPassword() tea.Cmd {
	cmds := []tea.Cmd{t.loadDetail()}
	if !t.msgs.loaded {
		t.msgs.gen++
		cmds = append(cmds, t.loadMsgs(0, t.msgs.pos, nil))
	}
	if t.section == secConsumers {
		cmds = append(cmds, t.loadConsumers())
	}
	return tea.Batch(cmds...)
}

func (t *topicTab) activeGrid() *grid.Model {
	switch t.section {
	case secPartitions:
		return t.partGrid
	case secConfig:
		return t.cfgGrid
	case secConsumers:
		return t.consGrid
	}
	return t.msgGrid
}

func (t *topicTab) Find(q string, forward bool) bool {
	g := t.activeGrid()
	return g != nil && g.Find(q, forward)
}

func (t *topicTab) loadDetail() tea.Cmd {
	t.loading = true
	tabCtx, id, b, conn, topic := t.ctx, t.id, t.backend, t.conn, t.topic
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		msg := topicLoadedMsg{tabID: id, conn: conn}
		msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
			var err error
			if msg.detail, err = c.TopicDetail(ctx, topic); err != nil {
				return err
			}
			msg.configs, msg.cfgErr = c.TopicConfigs(ctx, topic)
			return nil
		})
		return msg
	}
}

func (t *topicTab) loadConsumers() tea.Cmd {
	t.consLoading = true
	tabCtx, id, b, conn, topic := t.ctx, t.id, t.backend, t.conn, t.topic
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		msg := consumersLoadedMsg{tabID: id, conn: conn}
		msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
			var err error
			msg.groups, err = c.TopicGroups(ctx, topic)
			return err
		})
		return msg
	}
}

// kafkaErr is how a section shows err.
func kafkaErr(err error) string {
	switch {
	case errors.Is(err, kafka.ErrDenied):
		return strings.TrimPrefix(err.Error(), "kafka: ")
	case errors.Is(err, kafka.ErrUnsupported):
		return kafka.ErrUnsupported.Error()
	}
	return err.Error()
}

func (t *topicTab) Update(msg tea.Msg) tea.Cmd {
	if cmd, ok := t.msgsUpdate(msg); ok {
		return cmd
	}
	if cmd, ok := t.configsUpdate(msg); ok {
		return cmd
	}
	switch m := msg.(type) {
	case topicLoadedMsg:
		t.loading = false
		if m.err != nil {
			t.detailErr = kafkaErr(m.err)
			return nil
		}
		t.detailErr = ""
		t.detail = &m.detail
		t.configs, t.cfgErr = m.configs, ""
		if m.cfgErr != nil {
			t.configs, t.cfgErr = nil, kafkaErr(m.cfgErr)
		}
		t.partGrid.ReplaceResult(t.partitionsResult())
		t.cfgGrid.ReplaceResult(t.configResult())
	case consumersLoadedMsg:
		t.consLoading, t.consLoaded = false, true
		if m.err != nil {
			t.consErr = kafkaErr(m.err)
			t.consumers = nil
			t.consGrid.SetResult(nil)
			return nil
		}
		t.consErr, t.consumers = "", m.groups
		t.consGrid.ReplaceResult(t.consumersResult())
	}
	return nil
}

func (t *topicTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	if t.section == secMessages {
		if cmd, ok := t.handleMsgs(a); ok {
			return cmd
		}
	}
	if t.section == secConfig {
		if cmd, ok := t.handleConfig(a); ok {
			return cmd
		}
	}
	switch a {
	case keymap.NextSection:
		return t.setSection(min(t.section+1, topicSections-1))
	case keymap.PrevSection:
		return t.setSection(max(t.section-1, 0))
	case keymap.Refresh:
		switch t.section {
		case secConsumers:
			if !t.consLoading {
				return t.loadConsumers()
			}
		default:
			if !t.loading {
				return t.loadDetail()
			}
		}
		return nil
	case keymap.Open:
		if t.section == secConsumers {
			r, _ := t.consGrid.Position()
			if r < len(t.consumers) {
				return openGroupCmd(t.conn, t.consumers[r].Name)
			}
		}
		return nil
	}
	if g := t.activeGrid(); g != nil {
		if cmd, ok := gridAction(g, a); ok {
			return cmd
		}
	}
	return nil
}

func (t *topicTab) setSection(s topicSection) tea.Cmd {
	if s == t.section {
		return nil
	}
	t.section = s
	if s == secConsumers && !t.consLoaded && !t.consLoading {
		return t.loadConsumers()
	}
	return nil
}

// configValue is the topic's value of config name, "" when unknown.
func (t *topicTab) configValue(name string) string {
	for _, c := range t.configs {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func (t *topicTab) compacted() bool {
	return strings.Contains(t.configValue("cleanup.policy"), "compact")
}

// header is "12 partitions · RF 3 · 1.2 M msgs · retention 7d · cleanup delete".
func (t *topicTab) header() string {
	d := t.detail
	var msgs int64
	for _, p := range d.Parts {
		msgs += max(p.End-p.Start, 0)
	}
	approx := ""
	if t.compacted() {
		approx = "≈"
	}
	parts := []string{
		fmt.Sprintf("%d partitions", len(d.Parts)),
		fmt.Sprintf("RF %d", d.Replication),
		approx + humanCount(msgs) + " msgs",
	}
	if r := retentionText(t.configValue("retention.ms")); r != "" {
		parts = append(parts, r)
	}
	if c := t.configValue("cleanup.policy"); c != "" {
		parts = append(parts, "cleanup "+c)
	}
	return strings.Join(parts, " · ")
}

// humanCount is 999, 1.2 K, 1.2 M, 3.0 B.
func humanCount(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64) + " K"
	case n < 1_000_000_000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + " M"
	}
	return strconv.FormatFloat(float64(n)/1e9, 'f', 1, 64) + " B"
}

// retentionText is retention.ms as "retention 7d" (the largest whole
// unit), "retention ∞" for -1, "" when unknown.
func retentionText(ms string) string {
	n, err := strconv.ParseInt(ms, 10, 64)
	switch {
	case err != nil:
		return ""
	case n < 0:
		return "retention ∞"
	}
	for _, u := range []struct {
		ms   int64
		unit string
	}{{86_400_000, "d"}, {3_600_000, "h"}, {60_000, "m"}, {1000, "s"}} {
		if n >= u.ms && n%u.ms == 0 {
			return "retention " + strconv.FormatInt(n/u.ms, 10) + u.unit
		}
	}
	return "retention " + strconv.FormatInt(n, 10) + "ms"
}

func int32List(ids []int32) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.Itoa(int(id))
	}
	return strings.Join(s, ",")
}

func (t *topicTab) partitionsResult() *db.ResultSet {
	if t.detail == nil {
		return nil
	}
	approx := ""
	if t.compacted() {
		approx = "≈"
	}
	rows := make([][]string, len(t.detail.Parts))
	for i, p := range t.detail.Parts {
		leader := strconv.Itoa(int(p.Leader))
		if p.Leader < 0 {
			leader = "offline"
		}
		rows[i] = []string{
			strconv.Itoa(int(p.ID)), leader, int32List(p.Replicas), int32List(p.ISR),
			groupDigits(p.Start), groupDigits(p.End), approx + groupDigits(max(p.End-p.Start, 0)),
		}
	}
	return textResult([]string{"PART", "LEADER", "REPLICAS", "ISR", "START", "END", "MSGS"}, rows)
}

// partMark draws partitions short of in-sync replicas or without a
// leader in the error color.
func (t *topicTab) partMark(row, _ int) grid.Mark {
	if t.detail == nil || row >= len(t.detail.Parts) {
		return grid.MarkNone
	}
	if p := t.detail.Parts[row]; p.Leader < 0 || len(p.ISR) < len(p.Replicas) {
		return grid.MarkError
	}
	return grid.MarkNone
}

func (t *topicTab) consumersResult() *db.ResultSet {
	nparts := 0
	if t.detail != nil {
		nparts = len(t.detail.Parts)
	}
	for _, g := range t.consumers {
		for _, l := range g.Lag {
			nparts = max(nparts, int(l.Partition)+1)
		}
	}
	cols := []string{"GROUP", "STATE", "LAG"}
	for p := range nparts {
		cols = append(cols, "p"+strconv.Itoa(p))
	}
	rows := make([][]string, len(t.consumers))
	for i, g := range t.consumers {
		row := make([]string, len(cols))
		row[0], row[1] = g.Name, g.State
		var total int64
		for _, l := range g.Lag {
			total += l.Lag
			committed := "-"
			if l.Committed >= 0 {
				committed = groupDigits(l.Committed)
			}
			row[3+int(l.Partition)] = committed + "/" + groupDigits(l.Lag)
		}
		row[2] = groupDigits(total)
		rows[i] = row
	}
	return textResult(cols, rows)
}

func (t *topicTab) View(focused bool) string {
	names := make([]string, topicSections)
	for i, n := range topicSectionNames {
		if topicSection(i) == t.section {
			names[i] = theme.Title.Render(n)
		} else {
			names[i] = theme.Muted.Render(n)
		}
	}
	lines := []string{strings.Join(names, theme.Muted.Render(" · "))}
	switch {
	case t.detailErr != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.detailErr)))
	case t.detail != nil:
		lines = append(lines, theme.Muted.Render(t.header()))
		if note := t.cfgNote(); note != "" {
			lines = append(lines, note)
		}
	default:
		lines = append(lines, theme.Muted.Render("loading…"))
	}
	if t.status != "" {
		st := theme.Muted
		if t.statusE {
			st = theme.Error
		}
		lines = append(lines, st.Render(textutil.Sanitize(t.status)))
	}
	var g *grid.Model
	switch t.section {
	case secMessages:
		v := t.msgs
		switch {
		case v.err != "":
			lines = append(lines, theme.Error.Render(textutil.Sanitize(v.err)))
		case !v.loaded:
			lines = append(lines, theme.Muted.Render("loading…"))
		default:
			lines = append(lines, theme.Muted.Render(t.msgsHeader()))
			if len(v.recs) == 0 {
				lines = append(lines, theme.Muted.Render("no messages here"))
			} else {
				g = t.msgGrid
			}
		}
	case secPartitions:
		if t.detail != nil {
			g = t.partGrid
		}
	case secConfig:
		switch {
		case t.cfgErr != "":
			lines = append(lines, theme.Error.Render(textutil.Sanitize(t.cfgErr)))
		case t.configs != nil:
			g = t.cfgGrid
		}
	case secConsumers:
		switch {
		case t.consErr != "":
			lines = append(lines, theme.Error.Render(textutil.Sanitize(t.consErr)))
		case !t.consLoaded:
			lines = append(lines, theme.Muted.Render("loading…"))
		case len(t.consumers) == 0:
			lines = append(lines, theme.Muted.Render("no group commits offsets on this topic"))
		default:
			g = t.consGrid
		}
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
