package ui

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// kserverSection is one part of the Kafka cluster status tab.
type kserverSection int

const (
	secKStatus kserverSection = iota
	secKBrokers
	secKUnder
	kserverSections
)

var kserverSectionNames = [kserverSections]string{"Status", "Brokers", "Under-replicated"}

// lagGroupsMax is how many groups the status tab computes "groups with
// lag" for; above it the figure is not worth the requests.
const lagGroupsMax = 500

func kafkaServerTabKey(conn string) string { return "kserver\x00" + conn }

// openKafkaServer opens (or shows) the cluster status tab of conn.
func (m *Model) openKafkaServer(conn string) tea.Cmd {
	return m.showTab(kafkaServerTabKey(conn), func(id int) tab {
		return newKafkaServerTab(id, m.backend, m.keys, m.settings, conn, m.timings.kserver)
	})
}

// underRow is one partition of the Under-replicated section.
type underRow struct {
	topic string
	part  kafka.Partition
}

// kserverLoadedMsg is one reading of the cluster.
type kserverLoadedMsg struct {
	tabID, gen int
	conn       string
	cluster    kafka.ClusterInfo
	topics     []kafka.Topic
	groups     int
	withLag    int // -1: not computed (too many groups)
	under      []underRow
	err        error
}

func (m kserverLoadedMsg) TabID() int               { return m.tabID }
func (m kserverLoadedMsg) kafkaMsg()                {}
func (m kserverLoadedMsg) failure() (string, error) { return m.conn, m.err }

type kserverTickMsg struct{ tabID, gen int }

func (m kserverTickMsg) TabID() int { return m.tabID }

// openTopicSectionMsg opens a topic tab on one of its sections.
type openTopicSectionMsg struct {
	conn, topic string
	section     topicSection
}

// kafkaServerTab is a Kafka connection's cluster status: figures, brokers
// and the partitions short of replicas, read every interval while on
// screen.
type kafkaServerTab struct {
	id       int
	backend  Backend
	keys     *keymap.Keymap
	conn     string
	interval time.Duration
	section  kserverSection
	grids    [kserverSections]*grid.Model
	under    []underRow
	loaded   bool
	loading  bool
	visible  bool
	gen      int
	err      string

	width, height int
}

func newKafkaServerTab(id int, b Backend, keys *keymap.Keymap, s config.Settings, conn string, interval time.Duration) *kafkaServerTab {
	t := &kafkaServerTab{id: id, backend: b, keys: keys, conn: conn, interval: interval}
	for i := range t.grids {
		t.grids[i] = grid.New(nil, s.MaxColumnWidth)
	}
	return t
}

func (t *kafkaServerTab) ID() int                       { return t.id }
func (t *kafkaServerTab) Title() string                 { return t.conn + " · cluster" }
func (t *kafkaServerTab) Key() string                   { return kafkaServerTabKey(t.conn) }
func (t *kafkaServerTab) Context() keymap.Context       { return keymap.KafkaServer }
func (t *kafkaServerTab) Target() (string, db.TableRef) { return t.conn, db.TableRef{} }
func (t *kafkaServerTab) SetSize(w, h int)              { t.width, t.height = max(w, 0), max(h, 0) }
func (t *kafkaServerTab) Init() tea.Cmd                 { return t.load() }
func (t *kafkaServerTab) retryAfterPassword() tea.Cmd   { return t.load() }
func (t *kafkaServerTab) activeGrid() *grid.Model       { return t.grids[t.section] }

func (t *kafkaServerTab) Find(q string, forward bool) bool { return t.activeGrid().Find(q, forward) }

func (t *kafkaServerTab) exportSource() exportSrc {
	name := t.conn + "-" + strings.ToLower(strings.ReplaceAll(kserverSectionNames[t.section], " ", "-"))
	return exportSrc{rs: t.activeGrid().Result(), name: name, conn: t.conn}
}

// Shown reads the cluster at once and goes on refreshing.
func (t *kafkaServerTab) Shown() tea.Cmd {
	t.visible = true
	if t.loading {
		return nil
	}
	return t.load()
}

// Hidden stops the refreshes.
func (t *kafkaServerTab) Hidden() {
	t.visible = false
	t.gen++
	// A load in flight is dropped by gen, so Shown must start another.
	t.loading = false
}

func (t *kafkaServerTab) load() tea.Cmd {
	t.loading = true
	t.gen++
	id, gen, b, conn := t.id, t.gen, t.backend, t.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := kserverLoadedMsg{tabID: id, gen: gen, conn: conn, withLag: -1}
		msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
			var err error
			if msg.cluster, err = c.Cluster(ctx); err != nil {
				return err
			}
			if msg.topics, err = c.Topics(ctx); err != nil {
				return err
			}
			groups, err := c.Groups(ctx)
			if err != nil {
				return err
			}
			msg.groups = len(groups)
			var names []string
			for _, g := range groups {
				if g.Type.HasLag() {
					names = append(names, g.Name)
				}
			}
			if len(groups) <= lagGroupsMax {
				msg.withLag = 0
				for i := 0; i < len(names); i += lagBatch {
					lag, err := c.GroupsLag(ctx, names[i:min(i+lagBatch, len(names))])
					if err != nil {
						return err
					}
					for _, l := range lag {
						if l > 0 {
							msg.withLag++
						}
					}
				}
			}
			for _, tp := range msg.topics {
				if tp.UnderReplicated+tp.Offline == 0 {
					continue
				}
				d, err := c.TopicDetail(ctx, tp.Name)
				if err != nil {
					return err
				}
				for _, p := range d.Parts {
					if p.Leader < 0 || len(p.ISR) < len(p.Replicas) {
						msg.under = append(msg.under, underRow{topic: tp.Name, part: p})
					}
				}
			}
			return nil
		})
		return msg
	}
}

func (t *kafkaServerTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case kserverLoadedMsg:
		if m.gen != t.gen {
			return nil
		}
		t.loading = false
		if m.err != nil {
			t.err = kafkaErr(m.err)
			return nil
		}
		t.err, t.loaded = "", true
		t.apply(m)
		if !t.visible || t.interval <= 0 {
			return nil
		}
		id, gen := t.id, t.gen
		return tea.Tick(t.interval, func(time.Time) tea.Msg { return kserverTickMsg{tabID: id, gen: gen} })
	case kserverTickMsg:
		if m.gen == t.gen && t.visible && !t.loading {
			return t.load()
		}
	}
	return nil
}

func (t *kafkaServerTab) apply(m kserverLoadedMsg) {
	parts, under, offline := 0, 0, 0
	for _, tp := range m.topics {
		parts += tp.Partitions
		under += tp.UnderReplicated
		offline += tp.Offline
	}
	withLag := "n/a (> " + strconv.Itoa(lagGroupsMax) + " groups)"
	if m.withLag >= 0 {
		withLag = groupDigits(int64(m.withLag))
	}
	status := [][]string{
		{"cluster id", m.cluster.ID},
		{"controller", strconv.Itoa(int(m.cluster.Controller))},
		{"brokers", strconv.Itoa(len(m.cluster.Brokers))},
		{"version", m.cluster.Version},
		{"topics", groupDigits(int64(len(m.topics)))},
		{"partitions", groupDigits(int64(parts))},
		{"under-replicated partitions", groupDigits(int64(under))},
		{"offline partitions", groupDigits(int64(offline))},
		{"groups", groupDigits(int64(m.groups))},
		{"groups with lag", withLag},
	}
	var brokers [][]string
	for _, b := range m.cluster.Brokers {
		ctrl := ""
		if b.Controller {
			ctrl = "★"
		}
		brokers = append(brokers, []string{strconv.Itoa(int(b.ID)), b.Host + ":" + strconv.Itoa(int(b.Port)), b.Rack, ctrl, groupDigits(int64(b.Leaders))})
	}
	rows := make([][]string, len(m.under))
	for i, u := range m.under {
		leader := strconv.Itoa(int(u.part.Leader))
		if u.part.Leader < 0 {
			leader = "offline"
		}
		rows[i] = []string{u.topic, strconv.Itoa(int(u.part.ID)), leader, int32List(u.part.Replicas), int32List(u.part.ISR)}
	}
	t.under = m.under
	t.grids[secKStatus].ReplaceResult(textResult([]string{"metric", "value"}, status))
	t.grids[secKBrokers].ReplaceResult(textResult([]string{"ID", "HOST", "RACK", "CONTROLLER", "LEADERS"}, brokers))
	t.grids[secKUnder].ReplaceResult(textResult([]string{"TOPIC", "PART", "LEADER", "REPLICAS", "ISR"}, rows))
}

func (t *kafkaServerTab) Handle(a keymap.Action) tea.Cmd {
	switch a {
	case keymap.NextSection:
		t.section = min(t.section+1, kserverSections-1)
		return nil
	case keymap.PrevSection:
		t.section = max(t.section-1, 0)
		return nil
	case keymap.Refresh:
		if !t.loading {
			return t.load()
		}
		return nil
	case keymap.Open:
		if r, _ := t.grids[secKUnder].Position(); t.section == secKUnder && r < len(t.under) {
			msg := openTopicSectionMsg{conn: t.conn, topic: t.under[r].topic, section: secPartitions}
			return func() tea.Msg { return msg }
		}
		return nil
	}
	cmd, _ := gridAction(t.activeGrid(), a)
	return cmd
}

func (t *kafkaServerTab) View(focused bool) string {
	names := make([]string, kserverSections)
	for i, n := range kserverSectionNames {
		if kserverSection(i) == t.section {
			names[i] = theme.Title.Render(n)
		} else {
			names[i] = theme.Muted.Render(n)
		}
	}
	lines := []string{strings.Join(names, theme.Muted.Render(" · "))}
	var g *grid.Model
	switch {
	case t.err != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.err)))
	case !t.loaded:
		lines = append(lines, theme.Muted.Render("loading…"))
	case t.section == secKUnder && len(t.under) == 0:
		lines = append(lines, theme.Muted.Render("every partition has all its replicas in sync"))
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

// openTopicSection opens topic's tab on section.
func (m *Model) openTopicSection(msg openTopicSectionMsg) tea.Cmd {
	cmd := m.openTopic(msg.conn, msg.topic)
	if tt, ok := m.activeTab().(*topicTab); ok && tt.topic == msg.topic {
		return tea.Batch(cmd, tt.setSection(msg.section))
	}
	return cmd
}
