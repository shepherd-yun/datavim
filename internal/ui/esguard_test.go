package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/uitest"
)

func TestConfirmES(t *testing.T) {
	yes, no := true, false
	dev := config.Connection{Env: config.EnvDev, ReadOnly: &no}
	prod := config.Connection{Env: config.EnvProd, ReadOnly: &no}
	ro := config.Connection{Env: config.EnvDev, ReadOnly: &yes}
	write := esx.Classify("PUT", "orders/_doc/1", []byte(`{"a":1}`))
	quiet := esx.Classify("POST", "orders/_refresh", nil)
	danger := esx.Classify("DELETE", "orders", nil)
	critical := esx.Classify("DELETE", "logs-*", nil)
	read := esx.Classify("GET", "orders/_search", nil)
	cases := []struct {
		name    string
		cfg     config.Connection
		plan    esPlan
		refusal string
		run     bool
		expect  string   // the text a confirmation wants; "y" for y
		has     []string // in the confirmation
	}{
		{"read on read-only", ro, esPlan{Class: read}, "", true, "", nil},
		{"write on read-only", ro, esPlan{Class: write}, readOnlyHint, false, "", nil},
		{"danger on read-only", ro, esPlan{Class: danger}, readOnlyHint, false, "", nil},
		{"critical on read-only", ro, esPlan{Class: critical}, readOnlyHint, false, "", nil},
		{"write", dev, esPlan{Class: write, Lines: []string{"index document"}}, "", false, "y", []string{"index document"}},
		{"quiet write", dev, esPlan{Class: quiet}, "", true, "", nil},
		{"quiet write on prod", prod, esPlan{Class: quiet, Method: "POST", Path: "orders/_refresh"}, "", false, "y", []string{"POST orders/_refresh"}},
		{"prod write", prod, esPlan{Class: write, Method: "PUT", Path: "orders/_doc/1", Body: []byte("{\n  \"a\": 1\n}")}, "", false, "y", []string{"PUT orders/_doc/1", `"a": 1`}},
		{"prod password write", prod, esPlan{Class: esx.Classify("PUT", "_security/user/bob", nil), Method: "PUT", Path: "_security/user/bob", Body: []byte(`{"password":"pw1"}`)}, "", false, "y", []string{"PUT _security/user/bob", "<redacted>"}},
		{"danger", dev, esPlan{Class: danger}, "", false, "orders", []string{"type orders to confirm"}},
		{"danger on prod", prod, esPlan{Class: danger}, "", false, "orders", nil},
		{"critical", dev, esPlan{Class: critical, Resolved: []string{"logs-1", "logs-2"}}, "", false, "logs-*", []string{"logs-1", "logs-2", "type logs-* to confirm"}},
		{"critical on prod", prod, esPlan{Class: critical, Resolved: []string{"logs-1"}}, esProdRefusal, false, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := decideES(tc.cfg, tc.plan, "ok")
			if d.refusal != tc.refusal || d.run != tc.run {
				t.Fatalf("decision = %+v", d)
			}
			if tc.expect == "" {
				if d.confirm != nil {
					t.Fatalf("asks: %T", d.confirm)
				}
				return
			}
			c, ok := d.confirm.(*overlay.Confirm)
			if !ok {
				t.Fatalf("confirm = %T", d.confirm)
			}
			v := ansi.Strip(c.View(120, 40))
			if strings.Contains(v, "pw1") {
				t.Errorf("confirmation shows the password:\n%s", v)
			}
			for _, h := range tc.has {
				if !strings.Contains(v, h) {
					t.Errorf("confirmation lacks %q:\n%s", h, v)
				}
			}
			var cmd tea.Cmd
			if tc.expect == "y" {
				_, cmd = c.HandleKey(uitest.Key("y"))
			} else {
				for _, r := range tc.expect {
					c.HandleKey(uitest.Key(string(r)))
				}
				_, cmd = c.HandleKey(uitest.Key("enter"))
			}
			if cmd == nil || cmd() != "ok" {
				t.Errorf("confirming %q sent nothing", tc.expect)
			}
		})
	}
}
