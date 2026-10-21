package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
)

// openConfig opens orders' Config section; its configs are cleanup.policy
// (default), retention.ms (set) and segment.ms (set).
func openConfig(t *testing.T, cfg config.Connection) (*Model, *fakeKafka, *topicTab) {
	t.Helper()
	m, _, k := newKafkaModel(t, cfg)
	tp := k.addTopic("orders", 1, 0, 0)
	tp.configs = append(tp.configs, kafka.ConfigEntry{Name: "segment.ms", Value: "1000", Source: "topic"})
	tt := openTopicTab(t, m, "orders")
	pressAll(t, m, "]", "]")
	if tt.Context() != keymap.KafkaConfig {
		t.Fatalf("context = %s", tt.Context())
	}
	return m, k, tt
}

// editConfigTo replaces the cursor's config value with v.
func editConfigTo(t *testing.T, m *Model, v string) {
	t.Helper()
	pressAll(t, m, "i")
	if _, ok := m.overlay.(*overlay.CellEditor); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	pressAll(t, m, "esc", "c", "c")
	typeText(t, m, v)
	pressAll(t, m, "enter")
}

func TestConfigEditSubmit(t *testing.T) {
	m, k, tt := openConfig(t, config.Connection{})
	pressAll(t, m, "j") // retention.ms
	editConfigTo(t, m, "1")
	pressAll(t, m, "j", "d", "d") // segment.ms back to its default
	if s := screen(m); !strings.Contains(s, "2 changes") || !strings.Contains(s, "<space>w applies") {
		t.Fatalf("screen:\n%s", s)
	}
	pressAll(t, m, "space", "w")
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, tt.status)
	}
	v := c.View(100, 30)
	if !strings.Contains(v, "retention.ms  604800000 → 1") || !strings.Contains(v, "segment.ms  1000 → (default)") {
		t.Fatalf("confirm:\n%s", v)
	}
	pressAll(t, m, "y")
	drainAll(t, m, nil)
	if len(k.alters) != 1 || len(k.alters[0]) != 2 {
		t.Fatalf("alters = %+v", k.alters)
	}
	if tt.changes() != 0 {
		t.Fatal("the staged changes stay after applying them")
	}

	pressAll(t, m, "k")
	editConfigTo(t, m, "5")
	pressAll(t, m, "j", "d", "d", "u")
	pressAll(t, m, "space", "w", "y")
	drainAll(t, m, nil)
	if len(k.alters) != 2 || len(k.alters[1]) != 1 || k.alters[1][0].Name != "retention.ms" {
		t.Fatalf("after u: %+v", k.alters)
	}
}

func TestConfigMarks(t *testing.T) {
	m, _, tt := openConfig(t, config.Connection{})
	if tt.cfgMark(0, 0) != grid.MarkNone || tt.cfgMark(1, 0) != grid.MarkSet || tt.cfgMark(2, 0) != grid.MarkSet {
		t.Fatal("non-default configs are MarkSet")
	}
	pressAll(t, m, "j")
	editConfigTo(t, m, "1")
	pressAll(t, m, "j", "d", "d")
	if tt.cfgMark(1, 0) != grid.MarkChanged || tt.cfgMark(2, 0) != grid.MarkDeleted {
		t.Fatalf("marks %v %v", tt.cfgMark(1, 0), tt.cfgMark(2, 0))
	}
	if got := tt.cfgGrid.Result().Rows[1][1].Text; got != "1" {
		t.Fatalf("VALUE shows %q", got)
	}
	pressAll(t, m, "U")
	if tt.changes() != 0 || tt.cfgMark(1, 0) != grid.MarkSet {
		t.Fatal("U keeps changes")
	}
}

func TestConfigSensitiveRefused(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	tp := k.addTopic("orders", 1, 0, 0)
	tp.configs = append(tp.configs, kafka.ConfigEntry{Name: "sasl.jaas.config", Sensitive: true, Source: "topic"})
	tt := openTopicTab(t, m, "orders")
	pressAll(t, m, "]", "]", "G", "i")
	if m.overlay != nil || tt.status != "sensitive configs can't be edited here" {
		t.Fatalf("overlay %T status %q", m.overlay, tt.status)
	}
}

func TestConfigChangedKeepsEdits(t *testing.T) {
	m, k, tt := openConfig(t, config.Connection{})
	k.errs["AlterTopicConfigs"] = kafka.ErrConfigChanged
	pressAll(t, m, "j")
	editConfigTo(t, m, "1")
	before := len(k.callsOf("TopicConfigs"))
	pressAll(t, m, "space", "w", "y")
	drainAll(t, m, nil)
	if tt.changes() != 1 {
		t.Fatal("the staged change is gone")
	}
	if tt.status != kafka.ErrConfigChanged.Error() || !tt.statusE {
		t.Fatalf("status = %q", tt.status)
	}
	if len(k.callsOf("TopicConfigs")) == before {
		t.Fatal("the configs were not read again")
	}
}

func TestConfigEditReadOnly(t *testing.T) {
	on := true
	m, k, tt := openConfig(t, config.Connection{ReadOnly: &on})
	pressAll(t, m, "j")
	editConfigTo(t, m, "1")
	pressAll(t, m, "space", "w")
	if m.overlay != nil || tt.status != readOnlyHint {
		t.Fatalf("overlay %T status %q", m.overlay, tt.status)
	}
	if slices.ContainsFunc(k.calls, func(c string) bool { return strings.HasPrefix(c, "AlterTopicConfigs") }) {
		t.Fatal("the change was sent")
	}
}
