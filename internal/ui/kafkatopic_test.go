package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
)

// openTopicTab opens topic on the kafka model's connection and returns its tab.
func openTopicTab(t *testing.T, m *Model, topic string) *topicTab {
	t.Helper()
	drainAll(t, m, m.openTopic("events", topic))
	tt, ok := m.activeTab().(*topicTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	return tt
}

func TestTopicTabHeader(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 10)
	tt := openTopicTab(t, m, "orders")
	if tt.Title() != "orders" {
		t.Fatalf("title = %q", tt.Title())
	}
	if s := screen(m); !strings.Contains(s, "3 partitions · RF 2 · 30 msgs · retention 7d · cleanup delete") {
		t.Fatalf("screen:\n%s", s)
	}
	for in, want := range map[string]string{"-1": "retention ∞", "3600000": "retention 1h", "90000": "retention 90s", "": ""} {
		if got := retentionText(in); got != want {
			t.Errorf("retentionText(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[int64]string{999: "999", 1200: "1.2 K", 1_234_567: "1.2 M", 3_000_000_000: "3.0 B"} {
		if got := humanCount(in); got != want {
			t.Errorf("humanCount(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTopicTabSections(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 10)
	tt := openTopicTab(t, m, "orders")
	if tt.Context() != keymap.KafkaMessages {
		t.Fatalf("context = %s", tt.Context())
	}
	want := []topicSection{secPartitions, secConfig, secConsumers, secConsumers}
	for _, w := range want {
		pressAll(t, m, "]")
		if tt.section != w || tt.Context() != keymap.KafkaTopic {
			t.Fatalf("section = %d (want %d), context %s", tt.section, w, tt.Context())
		}
	}
	pressAll(t, m, "[", "[", "[", "[")
	if tt.section != secMessages {
		t.Fatalf("section = %d", tt.section)
	}
	if n := k.countCalls("TopicGroups"); n != 1 {
		t.Fatalf("Consumers loads when first shown: %d", n)
	}
}

func TestTopicTabPartitionsWarn(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	tp := k.addTopic("orders", 3, 0, 10)
	tp.detail.Parts[1].ISR = []int32{1}
	tp.detail.Parts[2].Leader = -1
	tp.configs[0].Value = "compact"
	tt := openTopicTab(t, m, "orders")
	pressAll(t, m, "]")
	s := screen(m)
	for _, w := range []string{"PART", "LEADER", "REPLICAS", "ISR", "START", "END", "MSGS", "≈10", "1,2"} {
		if !strings.Contains(s, w) {
			t.Fatalf("missing %q:\n%s", w, s)
		}
	}
	if tt.partMark(0, 0) != grid.MarkNone || tt.partMark(1, 0) != grid.MarkError || tt.partMark(2, 3) != grid.MarkError {
		t.Fatal("under-replicated and offline partitions are marked")
	}
}

func TestTopicTabConfigSensitive(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	tp := k.addTopic("orders", 1, 0, 0)
	tp.configs = append(tp.configs, kafka.ConfigEntry{Name: "sasl.jaas.config", Sensitive: true, Source: "topic"})
	tt := openTopicTab(t, m, "orders")
	pressAll(t, m, "]", "]")
	s := screen(m)
	if !strings.Contains(s, "NAME") || !strings.Contains(s, "******") || !strings.Contains(s, "604800000") {
		t.Fatalf("screen:\n%s", s)
	}
	// row 0 cleanup.policy is a default, row 1 retention.ms is set on the topic
	if tt.cfgMark(0, 0) != grid.MarkNone || tt.cfgMark(1, 0) != grid.MarkChanged {
		t.Fatal("non-default configs are highlighted")
	}
}

func TestTopicTabConsumersOpenGroup(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 2, 0, 10)
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "order-svc", State: "Empty"}, Lag: []kafka.PartitionLag{
		{TP: kafka.TP{Topic: "orders", Partition: 0}, Committed: 4, End: 10, Lag: 6},
		{TP: kafka.TP{Topic: "orders", Partition: 1}, Committed: -1, End: 10, Lag: 10},
	}})
	openTopicTab(t, m, "orders")
	pressAll(t, m, "]", "]", "]")
	s := screen(m)
	for _, w := range []string{"GROUP", "STATE", "LAG", "order-svc", "Empty", "16", "4/6", "-/10"} {
		if !strings.Contains(s, w) {
			t.Fatalf("missing %q:\n%s", w, s)
		}
	}
	pressAll(t, m, "enter")
	if !openedGroup(m, "order-svc") {
		t.Fatalf("enter opens the group; tab %T, status %q", m.activeTab(), m.status)
	}
}

// openedGroup reports whether the group was opened (its tab, once there
// is one).
func openedGroup(m *Model, group string) bool {
	return strings.Contains(m.status, group) || strings.Contains(m.activeTab().Title(), group)
}

func TestTopicTabDenied(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 2, 0, 10)
	k.setErr("TopicConfigs", kafka.ErrDenied)
	openTopicTab(t, m, "orders")
	if s := screen(m); !strings.Contains(s, "2 partitions") || strings.Contains(s, "retention") {
		t.Fatalf("header without configs:\n%s", s)
	}
	pressAll(t, m, "]")
	if s := screen(m); !strings.Contains(s, "PART") {
		t.Fatalf("partitions:\n%s", s)
	}
	pressAll(t, m, "]")
	if s := screen(m); !strings.Contains(s, "permission denied") {
		t.Fatalf("config:\n%s", s)
	}
}

func TestTopicTabReuse(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 1)
	openTopicTab(t, m, "orders")
	openTopicTab(t, m, "orders")
	if len(m.tabs) != 1 {
		t.Fatalf("%d tabs", len(m.tabs))
	}
	if topicTabKey("events", "orders") != "ktopic\x00events\x00orders" {
		t.Fatal("tab key")
	}
}
