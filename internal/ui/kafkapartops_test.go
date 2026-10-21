package ui

import (
	"maps"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

func TestAddPartitionsFlow(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 12, 0, 1)
	tt := openTopicTab(t, m, "orders")
	pressAll(t, m, "]", "o")
	p, ok := m.overlay.(*overlay.Prompt)
	if !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, tt.status)
	}
	if v := p.View(100, 20); !strings.Contains(v, "New total (now 12)") {
		t.Fatalf("prompt:\n%s", v)
	}
	typeText(t, m, "8")
	pressAll(t, m, "enter")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("8 partitions passed the prompt: %T", m.overlay)
	}
	pressAll(t, m, "backspace")
	typeText(t, m, "16")
	pressAll(t, m, "enter")
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	if v := c.View(100, 20); !strings.Contains(v, "orders: 12 → 16 partitions") || !strings.Contains(v, "Messages with a key may go to a different partition from now on.") {
		t.Fatalf("confirm:\n%s", v)
	}
	pressAll(t, m, "y")
	drainAll(t, m, nil)
	if got := k.callsOf("AddPartitions"); len(got) != 1 || got[0] != "AddPartitions orders 16" {
		t.Fatalf("calls = %v", got)
	}
}

// cursorOn puts the Messages cursor on partition p, offset o.
func cursorOn(t *testing.T, tt *topicTab, p int32, o int64) {
	t.Helper()
	for row, i := range tt.msgs.shown {
		if r := tt.msgs.recs[i]; r.Partition == p && r.Offset == o {
			tt.msgGrid.SetCursor(row, 0)
			return
		}
	}
	t.Fatalf("no message p%d:%d", p, o)
}

func TestDeleteRecordsFromMessage(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 2, 0, 10)
	tt := openTopicTab(t, m, "orders")
	cursorOn(t, tt, 1, 7)
	pressAll(t, m, "X")
	drainAll(t, m, nil)
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, tt.status)
	}
	if v := c.View(100, 20); !strings.Contains(v, "p1  0 → 8  (−8)") || confirmExpect(c) != "orders" {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "orders")
	pressAll(t, m, "enter")
	drainAll(t, m, nil)
	if len(k.deletes) != 1 || !maps.Equal(k.deletes[0], map[int32]int64{1: 8}) {
		t.Fatalf("deletes = %v", k.deletes)
	}
}

func TestDeleteRecordsFromPartitions(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 10)
	tt := openTopicTab(t, m, "orders")
	pressAll(t, m, "]", "X")
	if _, ok := m.overlay.(*overlay.Choice); !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, tt.status)
	}
	pressAll(t, m, "j", "enter") // all 3 partitions
	typeText(t, m, "-4")
	pressAll(t, m, "enter")
	drainAll(t, m, nil)
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, tt.status)
	}
	if v := c.View(100, 20); !strings.Contains(v, "p2  0 → 6  (−6)") {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "orders")
	pressAll(t, m, "enter")
	drainAll(t, m, nil)
	if len(k.deletes) != 1 || !maps.Equal(k.deletes[0], map[int32]int64{0: 6, 1: 6, 2: 6}) {
		t.Fatalf("deletes = %v", k.deletes)
	}
}

func TestDeleteRecordsUnsupported(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 10)
	k.unsupported = map[string]bool{"delete_records": true}
	tt := openTopicTab(t, m, "orders")
	cursorOn(t, tt, 0, 3)
	pressAll(t, m, "X")
	drainAll(t, m, nil)
	if m.overlay != nil || tt.status != "not supported by this cluster" {
		t.Fatalf("overlay %T status %q", m.overlay, tt.status)
	}
}

func TestDeleteRecordsReadOnly(t *testing.T) {
	on := true
	m, _, k := newKafkaModel(t, config.Connection{ReadOnly: &on})
	k.addTopic("orders", 1, 0, 10)
	tt := openTopicTab(t, m, "orders")
	pressAll(t, m, "X")
	if m.overlay != nil || tt.status != readOnlyHint {
		t.Fatalf("overlay %T status %q", m.overlay, tt.status)
	}
	pressAll(t, m, "]", "o")
	if m.overlay != nil || tt.status != readOnlyHint {
		t.Fatalf("o: overlay %T status %q", m.overlay, tt.status)
	}
}
