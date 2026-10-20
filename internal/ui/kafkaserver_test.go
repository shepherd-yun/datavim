package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"

	tea "charm.land/bubbletea/v2"
)

func openKafkaServer(t *testing.T, m *Model) *kafkaServerTab {
	t.Helper()
	m.setFocus(focusTree)
	pressAll(t, m, "space", "i")
	s, ok := m.activeTab().(*kafkaServerTab)
	if !ok {
		t.Fatalf("active tab = %T (%s)", m.activeTab(), m.status)
	}
	return s
}

func TestKafkaServerStatus(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	orders := k.addTopic("orders", 3, 0, 10)
	orders.detail.UnderReplicated, orders.detail.Parts[1].ISR = 1, []int32{1}
	k.addTopic("refunds", 2, 0, 10)
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "a"}, Lag: []kafka.PartitionLag{{Lag: 5}}})
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "b"}})
	s := openKafkaServer(t, m)
	if s.Title() != "events · cluster" || s.Context() != keymap.KafkaServer {
		t.Fatalf("title %q, context %s", s.Title(), s.Context())
	}
	view := screen(m)
	for _, w := range []string{
		"cluster id", "abc", "controller", "brokers", "version", "4.1", "topics", "partitions", "5",
		"under-replicated partitions", "offline partitions", "groups", "groups with lag",
	} {
		if !strings.Contains(view, w) {
			t.Fatalf("missing %q:\n%s", w, view)
		}
	}
	got := map[string]string{}
	for _, r := range s.grids[secKStatus].Result().Rows {
		got[r[0].Text] = r[1].Text
	}
	want := map[string]string{
		"brokers": "2", "topics": "2", "partitions": "5", "under-replicated partitions": "1",
		"offline partitions": "0", "groups": "2", "groups with lag": "1", "controller": "1", "cluster id": "abc", "version": "4.1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	pressAll(t, m, "]")
	if v := screen(m); !strings.Contains(v, "HOST") || !strings.Contains(v, "b1:9092") || !strings.Contains(v, "LEADERS") {
		t.Fatalf("brokers:\n%s", v)
	}
}

func TestKafkaServerUnderReplicatedOpen(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	orders := k.addTopic("orders", 3, 0, 10)
	orders.detail.UnderReplicated, orders.detail.Parts[1].ISR = 1, []int32{1}
	openKafkaServer(t, m)
	pressAll(t, m, "]", "]")
	if v := screen(m); !strings.Contains(v, "TOPIC") || !strings.Contains(v, "orders") || !strings.Contains(v, "1,2") {
		t.Fatalf("under-replicated:\n%s", v)
	}
	pressAll(t, m, "enter")
	tt, ok := m.activeTab().(*topicTab)
	if !ok || tt.topic != "orders" || tt.section != secPartitions {
		t.Fatalf("active tab %T", m.activeTab())
	}
}

func TestKafkaServerHiddenNoRefresh(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 1)
	s := openKafkaServer(t, m)
	calls := k.countCalls("Cluster")
	drainAll(t, m, func() tea.Msg { return kserverTickMsg{tabID: s.id, gen: s.gen} })
	if k.countCalls("Cluster") != calls+1 {
		t.Fatal("the tick reads the cluster again")
	}
	drainAll(t, m, m.openTopic("events", "orders"))
	calls = k.countCalls("Cluster")
	drainAll(t, m, func() tea.Msg { return kserverTickMsg{tabID: s.id, gen: s.gen} })
	if k.countCalls("Cluster") != calls {
		t.Fatal("no refresh while hidden")
	}
}

func TestKafkaServerManyGroups(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	for i := range 501 {
		k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: fmt.Sprintf("g%d", i)}})
	}
	m.setFocus(focusTree)
	pressAll(t, m, "l") // the tree computes lag itself
	k.lagCalls = nil
	s := openKafkaServer(t, m)
	got := map[string]string{}
	for _, r := range s.grids[secKStatus].Result().Rows {
		got[r[0].Text] = r[1].Text
	}
	if got["groups with lag"] != "n/a (> 500 groups)" || len(k.lagCalls) != 0 {
		t.Fatalf("groups with lag = %q, %d lag calls", got["groups with lag"], len(k.lagCalls))
	}
}

func TestKafkaNodeMenu(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 1)
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	root := m.tree.Roots()[0]
	descs := func(n *tree.Node) string {
		var out []string
		for _, it := range m.nodeMenu(n) {
			out = append(out, it.desc)
		}
		return strings.Join(out, ", ")
	}
	if d := descs(root); !strings.Contains(d, "Cluster status") || strings.Contains(d, "Open console") {
		t.Fatalf("connection menu: %s", d)
	}
	topics := section(t, m, "events", "topics")
	if d := descs(topics); !strings.Contains(d, "New topic") {
		t.Fatalf("topics menu: %s", d)
	}
	if d := descs(topics.Children[0]); !strings.Contains(d, "Producer console") || !strings.Contains(d, "Delete topic") {
		t.Fatalf("topic menu: %s", d)
	}
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Section == "topics" })
	pressAll(t, m, "a")
	if m.overlay == nil {
		t.Fatalf("a opens the menu on a kafka section: %q", m.status)
	}
}

func TestKafkaServerHiddenDuringLoad(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 1)
	m.setFocus(focusTree)
	_, load := m.Update(uitest.Key("space"))
	drainAll(t, m, load)
	_, load = m.Update(uitest.Key("i"))
	s := m.activeTab().(*kafkaServerTab)
	drainAll(t, m, m.openTopic("events", "orders"))
	drainAll(t, m, load)
	pressAll(t, m, "1")
	if s.loading || !s.loaded {
		t.Fatalf("shown again: loaded (loading=%v loaded=%v)", s.loading, s.loaded)
	}
}
