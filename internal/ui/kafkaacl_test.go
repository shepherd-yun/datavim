package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

func testACL(principal, typ, name, pattern string) kafka.ACL {
	return kafka.ACL{Principal: principal, Host: "*", ResourceType: typ, ResourceName: name, Pattern: pattern, Operation: "read", Permission: "allow"}
}

func openACLs(t *testing.T, m *Model) *aclTab {
	t.Helper()
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindKafkaACLs })
	pressAll(t, m, "enter")
	a, ok := m.activeTab().(*aclTab)
	if !ok {
		t.Fatalf("active tab = %T (%s)", m.activeTab(), m.status)
	}
	return a
}

func TestACLTabLoads(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.acls = []kafka.ACL{
		testACL("User:alice", "topic", "orders", "literal"),
		{Principal: "User:bob", Host: "*", ResourceType: "group", ResourceName: "g", Pattern: "literal", Operation: "all", Permission: "deny"},
	}
	a := openACLs(t, m)
	if a.Title() != "events · ACLs" || a.Context() != keymap.KafkaACL {
		t.Fatalf("title %q context %s", a.Title(), a.Context())
	}
	rs := a.grid.Result()
	if len(rs.Rows) != 2 || rs.Rows[0][0].Text != "User:alice" || rs.Rows[1][6].Text != "deny" {
		t.Fatalf("rows = %v", rs.Rows)
	}
	for i, c := range []string{"PRINCIPAL", "HOST", "RESOURCE", "NAME", "PATTERN", "OPERATION", "PERMISSION"} {
		if rs.Columns[i].Name != c {
			t.Fatalf("columns = %v", rs.Columns)
		}
	}
}

func TestACLTabNoAuthorizer(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.errs = map[string]error{"ACLs": kafka.ErrNoAuthorizer}
	openACLs(t, m)
	if v := screen(m); !strings.Contains(v, "ACLs are off") {
		t.Fatalf("screen:\n%s", v)
	}
}

func TestCreateACL(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	openACLs(t, m)
	pressAll(t, m, "o")
	if _, ok := m.overlay.(*overlay.ACLForm); !ok {
		t.Fatalf("overlay = %T (%s)", m.overlay, m.status)
	}
	typeText(t, m, "alice")
	pressAll(t, m, "tab", "tab", "tab")
	typeText(t, m, "orders")
	pressAll(t, m, "tab", "tab")
	typeText(t, m, ",describe")
	pressAll(t, m, "ctrl+s")
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T (%s)", m.overlay, m.status)
	}
	v := flatView(c)
	if !strings.Contains(v, "Create 2 ACLs?") || !strings.Contains(v, "User:alice ALLOW READ topic:orders (literal) from *") ||
		!strings.Contains(v, "User:alice ALLOW DESCRIBE topic:orders (literal) from *") {
		t.Fatalf("confirm:\n%s", v)
	}
	pressAll(t, m, "y")
	if got := k.callsOf("CreateACLs"); len(got) != 2 {
		t.Fatalf("calls = %v", got)
	}
	a := m.activeTab().(*aclTab)
	if r, _ := a.grid.Position(); len(a.acls) != 2 || a.acls[r].Operation != "describe" && a.acls[r].Operation != "read" {
		t.Fatalf("after create: %+v, cursor %d", a.acls, r)
	}
}

func TestCreateACLReadOnly(t *testing.T) {
	on := true
	m, _, _ := newKafkaModel(t, config.Connection{ReadOnly: &on})
	a := openACLs(t, m)
	pressAll(t, m, "o")
	if m.overlay != nil || !strings.Contains(a.status, "read-only") {
		t.Fatalf("overlay %T status %q", m.overlay, a.status)
	}
}

func TestDeleteACL(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.acls = []kafka.ACL{testACL("User:alice", "topic", "orders", "literal"), testACL("User:bob", "topic", "orders", "literal")}
	openACLs(t, m)
	pressAll(t, m, "j", "d", "d")
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T (%s)", m.overlay, m.status)
	}
	if confirmExpect(c) != "User:bob" || !strings.Contains(flatView(c), "User:bob ALLOW READ topic:orders (literal) from *") {
		t.Fatalf("confirm:\n%s", flatView(c))
	}
	typeText(t, m, "User:bob")
	pressAll(t, m, "enter")
	if got := k.callsOf("DeleteACLs"); len(got) != 1 || !strings.Contains(got[0], "User:bob") {
		t.Fatalf("calls = %v", got)
	}
	if a := m.activeTab().(*aclTab); len(a.acls) != 1 {
		t.Fatalf("acls after delete = %+v", a.acls)
	}
}

func TestDeleteACLMismatch(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.acls = []kafka.ACL{testACL("User:alice", "topic", "orders", "literal")}
	k.errs = map[string]error{"DeleteACLs": kafka.ErrACLMismatch}
	a := openACLs(t, m)
	pressAll(t, m, "d", "d")
	typeText(t, m, "User:alice")
	pressAll(t, m, "enter")
	if !strings.Contains(a.status, "deleted ACLs that were not selected") || !a.statusE {
		t.Fatalf("status = %q", a.status)
	}
}

func TestACLGoto(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 1)
	k.acls = []kafka.ACL{testACL("User:alice", "topic", "orders", "literal"), testACL("User:alice", "topic", "pay.", "prefixed")}
	openACLs(t, m)
	pressAll(t, m, "g", "d")
	if tt, ok := m.activeTab().(*topicTab); !ok || tt.topic != "orders" {
		t.Fatalf("active tab = %T (%s)", m.activeTab(), m.status)
	}
	a := openACLs(t, m)
	pressAll(t, m, "j", "g", "d")
	if m.activeTab() != a || !strings.Contains(a.status, "nothing to open") {
		t.Fatalf("active tab = %T, status %q", m.activeTab(), a.status)
	}
}
