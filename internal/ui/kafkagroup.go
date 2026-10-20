package ui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

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

// groupSection is one part of a group tab.
type groupSection int

const (
	secLag groupSection = iota
	secMembers
	groupSections
)

var groupSectionNames = [groupSections]string{"Lag", "Members"}

func groupTabKey(conn, group string) string { return "kgroup\x00" + conn + "\x00" + group }

// groupLoadedMsg is a group's members and lag.
type groupLoadedMsg struct {
	tabID, gen int
	conn       string
	detail     kafka.GroupDetail
	err        error
}

func (m groupLoadedMsg) TabID() int               { return m.tabID }
func (m groupLoadedMsg) kafkaMsg()                {}
func (m groupLoadedMsg) failure() (string, error) { return m.conn, m.err }

// groupTickMsg is time to read the group again.
type groupTickMsg struct{ tabID, gen int }

func (m groupTickMsg) TabID() int { return m.tabID }

// groupTab is a consumer group: its lag on every partition (read again
// every interval while on screen) and its members.
type groupTab struct {
	id       int
	backend  Backend
	keys     *keymap.Keymap
	conn     string
	group    string
	interval time.Duration // 0: no automatic refresh
	section  groupSection

	detail   *kafka.GroupDetail
	prevLag  map[kafka.TP]int64 // the lag before the last refresh, for ↑
	err      string
	loading  bool
	visible  bool
	gen      int // tells the current refresh cycle from stopped ones
	lagGrid  *grid.Model
	memGrid  *grid.Model
	status   string
	statusE  bool
	settings config.Settings

	width, height int
}

func newGroupTab(id int, b Backend, keys *keymap.Keymap, s config.Settings, conn, group string, interval time.Duration) *groupTab {
	return &groupTab{
		id: id, backend: b, keys: keys, settings: s, conn: conn, group: group, interval: interval,
		lagGrid: grid.New(nil, s.MaxColumnWidth), memGrid: grid.New(nil, s.MaxColumnWidth),
	}
}

// openGroup opens (or shows) the tab of group on conn.
func (m *Model) openGroup(conn, group string) tea.Cmd {
	rec := history.Recent{Kind: history.RecentKafkaGroup, Conn: conn, Name: group}
	return m.openTab(groupTabKey(conn, group), rec, func(id int) tab {
		return newGroupTab(id, m.backend, m.keys, m.settings, conn, group, m.timings.group)
	})
}

func (t *groupTab) ID() int                        { return t.id }
func (t *groupTab) Title() string                  { return t.group }
func (t *groupTab) Key() string                    { return groupTabKey(t.conn, t.group) }
func (t *groupTab) Context() keymap.Context        { return keymap.KafkaGroup }
func (t *groupTab) Target() (string, db.TableRef)  { return t.conn, db.TableRef{} }
func (t *groupTab) SetSize(w, h int)               { t.width, t.height = max(w, 0), max(h, 0) }
func (t *groupTab) Init() tea.Cmd                  { return t.load() }
func (t *groupTab) retryAfterPassword() tea.Cmd    { return t.load() }
func (t *groupTab) setStatus(s string, isErr bool) { t.status, t.statusE = s, isErr }

func (t *groupTab) activeGrid() *grid.Model {
	if t.section == secMembers {
		return t.memGrid
	}
	return t.lagGrid
}

func (t *groupTab) Find(q string, forward bool) bool { return t.activeGrid().Find(q, forward) }

func (t *groupTab) exportSource() exportSrc {
	name := t.group + "-" + strings.ToLower(groupSectionNames[t.section])
	return exportSrc{rs: t.activeGrid().Result(), name: name, conn: t.conn}
}

// Shown reads the group at once and goes on refreshing.
func (t *groupTab) Shown() tea.Cmd {
	t.visible = true
	if t.detail == nil || t.loading {
		return nil
	}
	return t.load()
}

// Hidden stops the refreshes.
func (t *groupTab) Hidden() {
	t.visible = false
	t.gen++
}

func (t *groupTab) load() tea.Cmd {
	t.loading = true
	t.gen++
	id, gen, b, conn, group := t.id, t.gen, t.backend, t.conn, t.group
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := groupLoadedMsg{tabID: id, gen: gen, conn: conn}
		msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
			var err error
			msg.detail, err = c.GroupDetail(ctx, group)
			return err
		})
		return msg
	}
}

// currentLag is the lag row under the cursor.
func (t *groupTab) currentLag() (kafka.PartitionLag, bool) {
	r, _ := t.lagGrid.Position()
	if t.detail == nil || r >= len(t.detail.Lag) {
		return kafka.PartitionLag{}, false
	}
	return t.detail.Lag[r], true
}

func (t *groupTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case groupLoadedMsg:
		if m.gen != t.gen {
			return nil
		}
		t.loading = false
		if m.err != nil {
			t.err = kafkaErr(m.err)
			return nil
		}
		t.err = ""
		t.apply(m.detail)
		if !t.visible || t.interval <= 0 {
			return nil
		}
		id, gen := t.id, t.gen
		return tea.Tick(t.interval, func(time.Time) tea.Msg { return groupTickMsg{tabID: id, gen: gen} })
	case groupTickMsg:
		if m.gen == t.gen && t.visible && !t.loading {
			return t.load()
		}
	case groupResetMsg:
		return t.reset(m)
	case groupResetDoneMsg:
		return t.resetDone(m)
	}
	return nil
}

// apply shows a newly read group, keeping the cursor on its partition.
func (t *groupTab) apply(d kafka.GroupDetail) {
	at, had := t.currentLag()
	if t.detail != nil {
		t.prevLag = map[kafka.TP]int64{}
		for _, l := range t.detail.Lag {
			t.prevLag[l.TP] = l.Lag
		}
	}
	t.detail = &d
	_, col := t.lagGrid.Position()
	t.lagGrid.SetResult(t.lagResult())
	if had {
		if i := slices.IndexFunc(d.Lag, func(l kafka.PartitionLag) bool { return l.TP == at.TP }); i >= 0 {
			t.lagGrid.SetCursor(i, col)
		}
	}
	t.memGrid.ReplaceResult(t.membersResult())
}

func (t *groupTab) lagResult() *db.ResultSet {
	rows := make([][]string, len(t.detail.Lag))
	for i, l := range t.detail.Lag {
		committed, lag := "-", groupDigits(l.Lag)
		if l.Committed >= 0 {
			committed = groupDigits(l.Committed)
		} else {
			lag += "*"
		}
		if prev, ok := t.prevLag[l.TP]; ok && l.Lag > prev {
			lag += " ↑"
		}
		rows[i] = []string{l.Topic, strconv.Itoa(int(l.Partition)), committed, groupDigits(l.End), lag, l.Member}
	}
	return textResult([]string{"TOPIC", "PART", "COMMITTED", "END", "LAG", "MEMBER"}, rows)
}

func (t *groupTab) membersResult() *db.ResultSet {
	rows := make([][]string, len(t.detail.Members))
	for i, mem := range t.detail.Members {
		rows[i] = []string{mem.ID, mem.ClientID, mem.Host, assignedText(mem.Assigned)}
	}
	return textResult([]string{"MEMBER", "CLIENT", "HOST", "PARTITIONS"}, rows)
}

// assignedText is "orders 0,1 · refunds 2".
func assignedText(tps []kafka.TP) string {
	var parts []string
	var topic string
	var ids []int32
	flush := func() {
		if topic != "" {
			parts = append(parts, topic+" "+int32List(ids))
		}
	}
	for _, tp := range tps {
		if tp.Topic != topic {
			flush()
			topic, ids = tp.Topic, nil
		}
		ids = append(ids, tp.Partition)
	}
	flush()
	return strings.Join(parts, " · ")
}

// header is "state Stable · members 3 · type classic · protocol range · total lag 48,212".
func (t *groupTab) header() string {
	d := t.detail
	var total int64
	for _, l := range d.Lag {
		total += l.Lag
	}
	parts := []string{"state " + d.State, "members " + strconv.Itoa(len(d.Members)), "type " + string(d.Type)}
	if d.Protocol != "" {
		parts = append(parts, "protocol "+d.Protocol)
	}
	return strings.Join(append(parts, "total lag "+groupDigits(total)), " · ")
}

func (t *groupTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	switch a {
	case keymap.NextSection:
		t.section = min(t.section+1, groupSections-1)
		return nil
	case keymap.PrevSection:
		t.section = max(t.section-1, 0)
		return nil
	case keymap.Refresh:
		if !t.loading {
			return t.load()
		}
		return nil
	case keymap.OpenCellKey:
		return t.gotoStuck()
	case keymap.ResetOffsets:
		return t.askReset()
	}
	if cmd, ok := gridAction(t.activeGrid(), a); ok {
		return cmd
	}
	return nil
}

// openTopicAtMsg asks the root model to show a topic's messages at pos.
type openTopicAtMsg struct {
	conn, topic, from string
	pos               kmsgx.Position
	note              string
}

// gotoStuck is gd on a lag row: the message the group is stuck on (its
// committed offset), or the partition's start when it never committed.
func (t *groupTab) gotoStuck() tea.Cmd {
	l, ok := t.currentLag()
	if t.section != secLag || !ok {
		return nil
	}
	msg := openTopicAtMsg{conn: t.conn, topic: l.Topic, from: t.group}
	switch {
	case l.Committed < 0:
		msg.pos = kmsgx.Position{Kind: kmsgx.PosEarliest, Partitions: []int32{l.Partition}}
	default:
		if l.Committed >= l.End {
			msg.note = "no lag on this partition"
		}
		msg.pos = kmsgx.Position{
			Kind: kmsgx.PosOffset, Partitions: []int32{l.Partition},
			Offsets: map[int32]int64{l.Partition: min(l.Committed, l.End)},
		}
	}
	return func() tea.Msg { return msg }
}

func (t *groupTab) View(focused bool) string {
	names := make([]string, groupSections)
	for i, n := range groupSectionNames {
		if groupSection(i) == t.section {
			names[i] = theme.Title.Render(n)
		} else {
			names[i] = theme.Muted.Render(n)
		}
	}
	lines := []string{strings.Join(names, theme.Muted.Render(" · "))}
	switch {
	case t.err != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.err)))
	case t.detail == nil:
		lines = append(lines, theme.Muted.Render("loading…"))
	default:
		lines = append(lines, theme.Muted.Render(textutil.Sanitize(t.header())))
	}
	if t.status != "" {
		st := theme.Muted
		if t.statusE {
			st = theme.Error
		}
		lines = append(lines, st.Render(textutil.Sanitize(t.status)))
	}
	var g *grid.Model
	if t.detail != nil {
		switch t.section {
		case secLag:
			if len(t.detail.Lag) == 0 {
				lines = append(lines, theme.Muted.Render(fmt.Sprintf("%s has no committed offsets", t.group)))
				break
			}
			if slices.ContainsFunc(t.detail.Lag, func(l kafka.PartitionLag) bool { return l.Committed < 0 }) {
				lines = append(lines, theme.Muted.Render("* never committed: lag from the start"))
			}
			g = t.lagGrid
		case secMembers:
			if len(t.detail.Members) == 0 {
				lines = append(lines, theme.Muted.Render("no active members"))
				break
			}
			g = t.memGrid
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

// openTopicAt shows topic's messages at pos, opened from group from ("":
// from nowhere).
func (m *Model) openTopicAt(conn, topic string, pos kmsgx.Position, from string) tea.Cmd {
	key := topicTabKey(conn, topic)
	for i, t := range m.tabs {
		if tt, ok := t.(*topicTab); ok && t.Key() == key {
			m.gotoTab(i)
			tt.section, tt.msgs.from = secMessages, from
			return tt.jump(pos)
		}
	}
	rec := history.Recent{Kind: history.RecentKafkaTopic, Conn: conn, Name: topic}
	return m.openTab(key, rec, func(id int) tab {
		t := newTopicTab(id, m.backend, m.keys, m.settings, conn, topic)
		t.msgs.pos, t.msgs.from = pos, from
		return t
	})
}

func (m *Model) openTopicAtMsg(msg openTopicAtMsg) tea.Cmd {
	cmd := m.openTopicAt(msg.conn, msg.topic, msg.pos, msg.from)
	if tt, ok := m.activeTab().(*topicTab); ok && msg.note != "" {
		tt.setStatus(msg.note, false)
	}
	return cmd
}
