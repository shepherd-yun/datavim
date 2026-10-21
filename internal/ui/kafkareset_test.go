package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

// emptyOrderSvc is orderSvc with its consumers stopped.
func emptyOrderSvc(k *fakeKafka) {
	orderSvc(k)
	d := k.groups["order-svc"]
	d.State, d.Members = "Empty", nil
	k.groups["order-svc"] = d
}

func TestResetReadOnlyRefused(t *testing.T) {
	ro := true
	m, _, k := newKafkaModel(t, config.Connection{ReadOnly: &ro})
	emptyOrderSvc(k)
	g := openGroupTab(t, m, "order-svc")
	pressAll(t, m, "R")
	if m.overlay != nil || g.status != readOnlyHint {
		t.Fatalf("overlay %T, status %q", m.overlay, g.status)
	}
	if k.countCalls("ResetOffsets") != 0 {
		t.Fatal("nothing is sent")
	}
}

func TestResetCommitsOnlyPreviewed(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	emptyOrderSvc(k)
	g := openGroupTab(t, m, "order-svc")
	pressAll(t, m, "j", "R") // p1, committed 150
	if _, ok := m.overlay.(*overlay.ResetOffsets); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	if s := screen(m); !strings.Contains(s, "orders p1  150 → 151 · lag 150 → 149") {
		t.Fatalf("screen:\n%s", s)
	}
	calls := k.countCalls("GroupDetail")
	pressAll(t, m, "enter", "y")
	if len(k.resets) != 1 || len(k.resets[0].commits) != 1 ||
		k.resets[0].commits[0] != (kafka.OffsetCommit{TP: kafka.TP{Topic: "orders", Partition: 1}, Expect: 150, To: 151}) {
		t.Fatalf("resets = %+v", k.resets)
	}
	if !strings.Contains(g.status, "reset 1 partition of order-svc") || k.countCalls("GroupDetail") != calls+1 {
		t.Fatalf("status %q, reloaded %v", g.status, k.countCalls("GroupDetail") == calls+1)
	}
}

func TestResetChanged(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	emptyOrderSvc(k)
	k.setErr("ResetOffsets", kafka.ErrGroupChanged)
	g := openGroupTab(t, m, "order-svc")
	pressAll(t, m, "j", "R", "enter", "y")
	if !strings.Contains(g.status, "group changed since preview; review again") || !g.statusE {
		t.Fatalf("status %q", g.status)
	}
}

func TestResetPartialFailure(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	emptyOrderSvc(k)
	k.resetResult = func(c []kafka.OffsetCommit) ([]kafka.CommitResult, error) {
		return []kafka.CommitResult{{TP: c[0].TP, Offset: c[0].To}, {TP: c[1].TP, Err: kafka.ErrDenied}, {TP: c[2].TP, Offset: c[2].To}}, nil
	}
	g := openGroupTab(t, m, "order-svc")
	pressAll(t, m, "j", "R", "right", "tab") // all of orders; to: next fails on p2, so earliest
	pressAll(t, m, "ctrl+u")
	typeText(t, m, "earliest")
	pressAll(t, m, "enter", "y")
	if !strings.Contains(g.status, "reset 2 of 3 partitions") || !strings.Contains(g.status, "orders p") || !g.statusE {
		t.Fatalf("status %q", g.status)
	}
}

func TestResetActiveGroupAndTypes(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	orderSvc(k) // Stable, one member
	openGroupTab(t, m, "order-svc")
	pressAll(t, m, "R")
	if s := screen(m); !strings.Contains(s, "group has 1 active members — stop the consumers first") {
		t.Fatalf("screen:\n%s", s)
	}
	pressAll(t, m, "esc")
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "sh", Type: kafka.GroupShare}})
	g := openGroupTab(t, m, "sh")
	pressAll(t, m, "R")
	if m.overlay != nil || g.status != "can't reset offsets of a share group" {
		t.Fatalf("overlay %T, status %q", m.overlay, g.status)
	}
}
