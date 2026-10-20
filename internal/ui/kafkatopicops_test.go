package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

// onTopics expands the kafka tree and puts the cursor on Topics.
func onTopics(t *testing.T, m *Model) {
	t.Helper()
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Section == "topics" })
}

func TestCreateTopicForm(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 1)
	onTopics(t, m)
	pressAll(t, m, "o")
	if _, ok := m.overlay.(*overlay.TopicForm); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, "audit")
	pressAll(t, m, "tab", "backspace", "4", "ctrl+s")
	if _, ok := m.overlay.(*overlay.Confirm); !ok || !strings.Contains(screen(m), "Create topic audit with 4 partitions") {
		t.Fatalf("overlay %T:\n%s", m.overlay, screen(m))
	}
	pressAll(t, m, "y")
	if len(k.created) != 1 || k.created[0].Name != "audit" || k.created[0].Partitions != 4 || k.created[0].Replication != -1 {
		t.Fatalf("created = %+v", k.created)
	}
	if n := m.tree.Selected(); n.Kind != tree.KindTopic || n.Key != "audit" {
		t.Fatalf("selected %+v", n)
	}
}

func TestCreateTopicInvalidName(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 1)
	onTopics(t, m)
	pressAll(t, m, "o")
	typeText(t, m, "bad name")
	pressAll(t, m, "ctrl+s")
	if _, ok := m.overlay.(*overlay.TopicForm); !ok || len(k.created) != 0 {
		t.Fatalf("overlay %T, created %v", m.overlay, k.created)
	}
}

func TestDeleteTopicTypeName(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 12, 0, 100)
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "a"}, Lag: []kafka.PartitionLag{{TP: kafka.TP{Topic: "orders"}}}})
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "b"}, Lag: []kafka.PartitionLag{{TP: kafka.TP{Topic: "orders"}}}})
	onTopics(t, m)
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindTopic })
	pressAll(t, m, "d", "d")
	if _, ok := m.overlay.(*overlay.Confirm); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	var flat strings.Builder // the box's lines joined, wrapping undone
	for _, l := range strings.Split(ansi.Strip(m.overlay.View(300, 40)), "\n") {
		flat.WriteString(strings.TrimRight(strings.Trim(strings.TrimSpace(l), "│"), " "))
	}
	if s := flat.String(); !strings.Contains(s, "Delete topic orders? 12 partitions · 1.2 K messages · consumed by 2 groups · this can't be undone.") {
		t.Fatalf("overlay %T:\n%s", m.overlay, s)
	}
	pressAll(t, m, "y", "enter")
	if len(k.deleted) != 0 {
		t.Fatal("y is not enough: the name must be typed")
	}
	pressAll(t, m, "backspace")
	typeText(t, m, "orders")
	pressAll(t, m, "enter")
	if len(k.deleted) != 1 || k.deleted[0] != "orders" {
		t.Fatalf("deleted = %v", k.deleted)
	}
	if got := names(section(t, m, "events", "topics").Children); len(got) != 0 {
		t.Fatalf("topics after delete = %v", got)
	}
}

func TestDeleteTopicClosesTabs(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 10)
	drainAll(t, m, m.openTopic("events", "orders"))
	drainAll(t, m, m.openProducer("events", "orders"))
	m.activeTab().(*producerTab).editor.SetValue("key: keep me\n\n{}")
	onTopics(t, m)
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindTopic })
	pressAll(t, m, "d", "d")
	typeText(t, m, "orders")
	pressAll(t, m, "enter")
	if len(m.tabs) != 0 {
		t.Fatalf("%d tabs left", len(m.tabs))
	}
	if d := m.store.(*fakeStore).draft(producerDraftKey("events", "orders")); d != "key: keep me\n\n{}" {
		t.Fatalf("draft = %q", d)
	}
}

func TestTopicOpsReadOnly(t *testing.T) {
	on := true
	m, _, k := newKafkaModel(t, config.Connection{ReadOnly: &on})
	k.addTopic("orders", 1, 0, 10)
	onTopics(t, m)
	pressAll(t, m, "o")
	if m.overlay != nil || m.status != readOnlyHint {
		t.Fatalf("create: overlay %T, status %q", m.overlay, m.status)
	}
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindTopic })
	pressAll(t, m, "d", "d")
	if m.overlay != nil || m.status != readOnlyHint {
		t.Fatalf("delete: overlay %T, status %q", m.overlay, m.status)
	}
	// dd on the connection still deletes the connection
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindConn })
	pressAll(t, m, "d", "d")
	if _, ok := m.overlay.(*overlay.Confirm); !ok || !strings.Contains(screen(m), "Delete connection") {
		t.Fatalf("overlay %T", m.overlay)
	}
}
