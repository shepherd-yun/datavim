package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// flatView is o's box with its lines joined, wrapping undone.
func flatView(o interface{ View(int, int) string }) string {
	var flat strings.Builder
	for _, l := range strings.Split(ansi.Strip(o.View(300, 40)), "\n") {
		flat.WriteString(strings.TrimRight(strings.Trim(strings.TrimSpace(l), "│"), " "))
	}
	return flat.String()
}

func lagOn(topics ...string) []kafka.PartitionLag {
	var out []kafka.PartitionLag
	for _, tp := range topics {
		out = append(out, kafka.PartitionLag{TP: kafka.TP{Topic: tp}, Committed: 1, End: 2, Lag: 1})
	}
	return out
}

// onGroup selects group in the tree.
func onGroup(t *testing.T, m *Model, group string) {
	t.Helper()
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Section == "groups" })
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindKafkaGroup && n.Key == group })
}

func TestDeleteGroupFromTree(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 1)
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "billing", State: "Empty", Type: kafka.GroupClassic}, Lag: lagOn("orders", "refunds")})
	drainAll(t, m, m.openGroup("events", "billing"))
	onGroup(t, m, "billing")
	pressAll(t, m, "d", "d")
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, m.status)
	}
	if v := flatView(c); !strings.Contains(v, "Delete group billing? It has committed offsets on 2 topics · this can't be undone.") || confirmExpect(c) != "billing" {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "billing")
	pressAll(t, m, "enter")
	if got := k.callsOf("DeleteGroup"); len(got) != 1 {
		t.Fatalf("calls = %v", got)
	}
	if len(m.tabs) != 0 {
		t.Fatal("the group's tab stays open")
	}
	if got := names(section(t, m, "events", "groups").Children); len(got) != 0 {
		t.Fatalf("groups after the delete = %v", got)
	}
}

func TestDeleteGroupNotEmpty(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addGroup(kafka.GroupDetail{
		Group:   kafka.Group{Name: "billing", State: "Stable", Type: kafka.GroupClassic},
		Members: []kafka.Member{{ID: "1"}, {ID: "2"}, {ID: "3"}}, Lag: lagOn("orders"),
	})
	onGroup(t, m, "billing")
	pressAll(t, m, "d", "d")
	if m.overlay != nil || m.status != "group has 3 active members — stop the consumers first" {
		t.Fatalf("overlay %T status %q", m.overlay, m.status)
	}
}

func TestDeleteGroupShareRefused(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "sg", State: "Empty", Type: kafka.GroupShare}})
	onGroup(t, m, "sg")
	pressAll(t, m, "d", "d")
	if m.overlay != nil || m.status != "can't delete a share group here" {
		t.Fatalf("overlay %T status %q", m.overlay, m.status)
	}
}

func TestDeleteGroupRaced(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "billing", State: "Empty", Type: kafka.GroupClassic}, Lag: lagOn("orders")})
	k.errs["DeleteGroup"] = kafka.ErrGroupNotEmpty
	onGroup(t, m, "billing")
	pressAll(t, m, "d", "d")
	typeText(t, m, "billing")
	pressAll(t, m, "enter")
	if m.status != "delete group billing: "+kafka.ErrGroupNotEmpty.Error() {
		t.Fatalf("status = %q", m.status)
	}
	if got := names(section(t, m, "events", "groups").Children); len(got) != 1 {
		t.Fatalf("groups = %v", got)
	}
}

func TestDeleteGroupOffsets(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	lag := lagOn("orders", "orders", "refunds")
	lag[1].Partition = 1
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "billing", State: "Empty", Type: kafka.GroupClassic}, Lag: lag})
	drainAll(t, m, m.openGroup("events", "billing"))
	gt := m.activeTab().(*groupTab)
	gt.lagGrid.SetCursor(slices.IndexFunc(gt.detail.Lag, func(l kafka.PartitionLag) bool { return l.Topic == "orders" }), 0)
	pressAll(t, m, "X")
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, gt.status)
	}
	if v := flatView(c); !strings.Contains(v, "Delete billing's offsets on orders (2 partitions)? The group starts from its auto.offset.reset next time.") || confirmExpect(c) != "billing" {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "billing")
	pressAll(t, m, "enter")
	if got := k.callsOf("DeleteGroupOffsets"); len(got) != 1 || got[0] != "DeleteGroupOffsets billing orders" {
		t.Fatalf("calls = %v", got)
	}
}
