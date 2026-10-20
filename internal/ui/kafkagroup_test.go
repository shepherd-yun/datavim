package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"

	tea "charm.land/bubbletea/v2"
)

// orderSvc is a group stuck on orders p1 (committed 150 of 300), behind
// on p0 and never committed on p2.
func orderSvc(k *fakeKafka) {
	k.addTopic("orders", 3, 0, 300)
	k.addGroup(kafka.GroupDetail{
		Group: kafka.Group{Name: "order-svc", State: "Stable", Protocol: "range"},
		Members: []kafka.Member{
			{ID: "m-1", ClientID: "svc-1", Host: "/10.0.0.1", Assigned: []kafka.TP{{Topic: "orders", Partition: 0}, {Topic: "orders", Partition: 1}}},
		},
		Lag: []kafka.PartitionLag{
			{TP: kafka.TP{Topic: "orders", Partition: 0}, Committed: 290, Start: 0, End: 300, Lag: 10, Member: "svc-1"},
			{TP: kafka.TP{Topic: "orders", Partition: 1}, Committed: 150, Start: 0, End: 300, Lag: 150, Member: "svc-1"},
			{TP: kafka.TP{Topic: "orders", Partition: 2}, Committed: -1, Start: 0, End: 300, Lag: 300},
		},
	})
}

func openGroupTab(t *testing.T, m *Model, group string) *groupTab {
	t.Helper()
	drainAll(t, m, m.openGroup("events", group))
	g, ok := m.activeTab().(*groupTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	return g
}

func TestGroupTabLag(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	orderSvc(k)
	g := openGroupTab(t, m, "order-svc")
	if g.Title() != "order-svc" || g.Context() != keymap.KafkaGroup {
		t.Fatalf("title %q context %s", g.Title(), g.Context())
	}
	s := screen(m)
	for _, w := range []string{
		"state Stable · members 1 · type classic · protocol range · total lag 460",
		"TOPIC", "PART", "COMMITTED", "END", "LAG", "MEMBER", "300*", "* never committed: lag from the start",
	} {
		if !strings.Contains(s, w) {
			t.Fatalf("missing %q:\n%s", w, s)
		}
	}
	rs := g.lagGrid.Result()
	if rs.Rows[0][1].Text != "2" || rs.Rows[0][2].Text != "-" || rs.Rows[1][1].Text != "1" {
		t.Fatalf("rows by lag: %v", rs.Rows)
	}
}

func TestGroupTabRefresh(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	orderSvc(k)
	g := openGroupTab(t, m, "order-svc")
	pressAll(t, m, "j") // p1
	d := k.groups["order-svc"]
	d.Lag[1].End, d.Lag[1].Lag = 400, 250 // p1 now the largest
	k.groups["order-svc"] = d
	calls := k.countCalls("GroupDetail")
	drainAll(t, m, func() tea.Msg { return groupTickMsg{tabID: g.id, gen: g.gen} })
	if k.countCalls("GroupDetail") != calls+1 {
		t.Fatal("the tick reads the group again")
	}
	if l, ok := g.currentLag(); !ok || l.Partition != 1 {
		t.Fatalf("cursor on %+v", l)
	}
	if s := screen(m); !strings.Contains(s, "250 ↑") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestGroupTabHiddenNoRefresh(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	orderSvc(k)
	g := openGroupTab(t, m, "order-svc")
	drainAll(t, m, m.openTopic("events", "orders")) // hides the group
	calls := k.countCalls("GroupDetail")
	drainAll(t, m, func() tea.Msg { return groupTickMsg{tabID: g.id, gen: g.gen} })
	if k.countCalls("GroupDetail") != calls {
		t.Fatal("no refresh while hidden")
	}
	pressAll(t, m, "1")
	if k.countCalls("GroupDetail") != calls+1 {
		t.Fatal("shown again: refreshed at once")
	}
}

func TestGroupGdJumpsToCommitted(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	orderSvc(k)
	openGroupTab(t, m, "order-svc")
	pressAll(t, m, "j", "g", "d") // p1, committed 150
	tt, ok := m.activeTab().(*topicTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	c := cursorRec(t, tt)
	row, _ := tt.msgGrid.Position()
	if c.Partition != 1 || c.Offset != 150 || row != 3 {
		t.Fatalf("cursor %+v at row %d", c, row)
	}
	if s := screen(m); !strings.Contains(s, "from group order-svc") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestGroupGdNeverCommitted(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	orderSvc(k)
	openGroupTab(t, m, "order-svc")
	pressAll(t, m, "g", "d") // p2, never committed
	tt := m.activeTab().(*topicTab)
	if c := cursorRec(t, tt); c.Partition != 2 || c.Offset != 0 || !tt.msgs.single {
		t.Fatalf("cursor %+v", c)
	}
}

func TestGroupGdNoLag(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	orderSvc(k)
	d := k.groups["order-svc"]
	d.Lag[0].Committed, d.Lag[0].Lag = 300, 0
	k.groups["order-svc"] = d
	g := openGroupTab(t, m, "order-svc")
	selectLagRow(t, g, 0)
	pressAll(t, m, "g", "d")
	tt := m.activeTab().(*topicTab)
	if c := cursorRec(t, tt); c.Partition != 0 || c.Offset != 299 {
		t.Fatalf("cursor %+v", c)
	}
	if !strings.Contains(m.status+tt.status, "no lag on this partition") {
		t.Fatalf("status %q / %q", m.status, tt.status)
	}
}

// selectLagRow puts the lag cursor on partition p.
func selectLagRow(t *testing.T, g *groupTab, p int32) {
	t.Helper()
	for i, l := range g.detail.Lag {
		if l.Partition == p {
			_, col := g.lagGrid.Position()
			g.lagGrid.SetCursor(i, col)
			return
		}
	}
	t.Fatalf("no row for p%d", p)
}

func TestMessagesGdBackToGroup(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	orderSvc(k)
	openGroupTab(t, m, "order-svc")
	pressAll(t, m, "g", "d")
	if _, ok := m.activeTab().(*topicTab); !ok {
		t.Fatal("gd opens the topic")
	}
	pressAll(t, m, "g", "d")
	if g, ok := m.activeTab().(*groupTab); !ok || g.group != "order-svc" {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	k.addTopic("refunds", 1, 0, 1)
	tt := openMessages(t, m, "refunds")
	pressAll(t, m, "g", "d")
	if !strings.Contains(tt.status, "this view didn't come from a group") {
		t.Fatalf("status %q", tt.status)
	}
}

func TestGroupMembers(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	orderSvc(k)
	openGroupTab(t, m, "order-svc")
	pressAll(t, m, "]")
	s := screen(m)
	for _, w := range []string{"MEMBER", "CLIENT", "HOST", "PARTITIONS", "m-1", "svc-1", "/10.0.0.1", "orders 0,1"} {
		if !strings.Contains(s, w) {
			t.Fatalf("missing %q:\n%s", w, s)
		}
	}
}

func TestGroupTabHiddenDuringLoad(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	orderSvc(k)
	k.addTopic("refunds", 1, 0, 1)
	_, load := m.Update(openGroupMsg{conn: "events", group: "order-svc"})
	g := m.activeTab().(*groupTab)
	drainAll(t, m, m.openTopic("events", "refunds")) // gd and tab switches hide it mid-load
	drainAll(t, m, load)                             // the stale load lands
	pressAll(t, m, "1")
	if g.loading || g.detail == nil {
		t.Fatalf("shown again: loaded (loading=%v detail=%v)", g.loading, g.detail != nil)
	}
}
