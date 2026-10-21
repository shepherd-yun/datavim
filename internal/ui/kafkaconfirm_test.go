package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

type confirmedMsg struct{}

// confirmExpect is the name c wants typed, "" when y confirms.
func confirmExpect(c *overlay.Confirm) string {
	v := ansi.Strip(c.View(100, 30))
	if i := strings.Index(v, "Type "); i >= 0 {
		return strings.Fields(v[i+len("Type "):])[0]
	}
	return ""
}

func TestKafkaConfirm(t *testing.T) {
	on, off := true, false
	b := newFakeBackend()
	b.conns = []config.Connection{
		{Name: "ro", Driver: "kafka", ReadOnly: &on},
		{Name: "dev", Driver: "kafka", Env: config.EnvDev},
		{Name: "prod", Driver: "kafka", Env: config.EnvProd, ReadOnly: &off},
	}
	if cmd, why := kafkaConfirm(b, "ro", kmsgx.OpAlterConfigs, "t", nil, "", confirmedMsg{}); cmd != nil || why != readOnlyHint {
		t.Fatalf("read-only: %v %q", cmd, why)
	}
	expectOf := func(conn string, op kmsgx.Op) (*overlay.Confirm, tea.Msg) {
		t.Helper()
		cmd, why := kafkaConfirm(b, conn, op, "Title", []string{"line"}, "orders", confirmedMsg{})
		if why != "" || cmd == nil {
			t.Fatalf("%s %v: %v %q", conn, op, cmd, why)
		}
		msg := cmd()
		if o, ok := msg.(openOverlayMsg); ok {
			c, _ := o.overlay.(*overlay.Confirm)
			return c, nil
		}
		return nil, msg
	}
	if c, _ := expectOf("dev", kmsgx.OpAlterConfigs); c == nil || confirmExpect(c) != "" {
		t.Fatalf("dev alter configs: %+v", c)
	}
	if c, _ := expectOf("prod", kmsgx.OpDeleteGroup); c == nil || confirmExpect(c) != "orders" {
		t.Fatalf("delete group: %+v", c)
	}
	if c, msg := expectOf("dev", kmsgx.OpProduce); c != nil || msg != (confirmedMsg{}) {
		t.Fatalf("dev produce: %+v %v", c, msg)
	}
}
