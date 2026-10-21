package overlay

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
)

func TestACLForm(t *testing.T) {
	f := NewACLForm(keymap.Default())
	typeIn(t, f, "alice")    // after the "User:" default
	send(t, f, "tab", "tab") // host stays *
	send(t, f, "tab")        // resource: topic
	typeIn(t, f, "orders")
	send(t, f, "tab", "right") // pattern: prefixed
	send(t, f, "tab", "backspace", "backspace", "backspace", "backspace")
	typeIn(t, f, "read, describe")
	send(t, f, "tab", "right") // permission: deny
	_, msg := send(t, f, "ctrl+s")
	got, ok := msg.(ACLFormMsg)
	if !ok {
		t.Fatalf("msg = %#v\n%s", msg, ansi.Strip(f.View(100, 40)))
	}
	base := kafka.ACL{Principal: "User:alice", Host: "*", ResourceType: "topic", ResourceName: "orders", Pattern: "prefixed", Permission: "deny"}
	read, describe := base, base
	read.Operation, describe.Operation = "read", "describe"
	if !slices.Equal(got.ACLs, []kafka.ACL{read, describe}) {
		t.Fatalf("acls = %+v", got.ACLs)
	}
}

func TestACLFormCluster(t *testing.T) {
	f := NewACLForm(keymap.Default())
	typeIn(t, f, "ops")
	send(t, f, "tab", "tab", "right", "right") // resource: topic -> group -> cluster
	if !strings.Contains(ansi.Strip(f.View(100, 40)), "kafka-cluster") {
		t.Fatalf("cluster name not shown:\n%s", ansi.Strip(f.View(100, 40)))
	}
	_, msg := send(t, f, "ctrl+s")
	got := msg.(ACLFormMsg).ACLs
	if len(got) != 1 || got[0].ResourceType != "cluster" || got[0].ResourceName != "kafka-cluster" {
		t.Fatalf("acls = %+v", got)
	}
}

func TestACLFormValidation(t *testing.T) {
	for _, tc := range []struct {
		edit func(f *ACLForm)
		want string
	}{
		{func(f *ACLForm) {
			send(t, f, "backspace", "backspace", "backspace", "backspace", "backspace")
			typeIn(t, f, "alice")
		}, "principal"},
		{func(f *ACLForm) { typeIn(t, f, "alice"); send(t, f, "tab", "tab", "tab") }, "name is required"},
		{func(f *ACLForm) {
			typeIn(t, f, "alice")
			send(t, f, "tab", "tab", "tab")
			typeIn(t, f, "orders")
			send(t, f, "tab", "tab")
			typeIn(t, f, ",fly")
		}, `"fly"`},
	} {
		f := NewACLForm(keymap.Default())
		tc.edit(f)
		o, msg := send(t, f, "ctrl+s")
		if msg != nil || o != f {
			t.Fatalf("submitted: %#v", msg)
		}
		if view := ansi.Strip(f.View(100, 40)); !strings.Contains(view, tc.want) {
			t.Errorf("want %q in\n%s", tc.want, view)
		}
	}
}
