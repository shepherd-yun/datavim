package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/config"
)

func TestGuardSession(t *testing.T) {
	target := sessionTarget{ID: "48211", User: "app", Host: "10.0.3.21", DB: "shop", State: "active", Elapsed: "4m12s", SQL: "SELECT\n  1"}
	dev := config.Connection{Name: "dev-db", Env: config.EnvDev}
	cases := []struct {
		name   string
		cfg    config.Connection
		act    sessionAction
		target sessionTarget
		refuse bool
		title  string
		expect string
		lines  []string // must be in Body
		absent []string // must not be in Body
	}{
		{name: "read-only dev", cfg: config.Connection{Name: "ro", ReadOnly: boolPtr(true)}, act: actDisconnect, target: target, refuse: true},
		{name: "prod is read-only by default", cfg: config.Connection{Name: "prod-db", Env: config.EnvProd}, act: actCancel, target: target, refuse: true},
		{
			name: "writable prod types the name", cfg: config.Connection{Name: "prod-db", Env: config.EnvProd, ReadOnly: boolPtr(false)},
			act: actDisconnect, target: target, title: "Disconnect session 48211?", expect: "prod-db",
		},
		{
			name: "dev cancel", cfg: dev, act: actCancel, target: target, title: "Cancel the query of session 48211?",
			lines:  []string{"user app · host 10.0.3.21 · db shop", "active · 4m12s", "SELECT 1"},
			absent: []string{"Its open transaction, if any, is rolled back.", "This is a datavim connection (maybe this one)."},
		},
		{
			name: "dev disconnect of datavim's own", cfg: dev, act: actDisconnect,
			target: func() sessionTarget { s := target; s.Self = true; return s }(),
			title:  "Disconnect session 48211?",
			lines:  []string{"Its open transaction, if any, is rolled back.", "This is a datavim connection (maybe this one)."},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := guardSession(c.cfg, c.act, c.target, 60)
			if c.refuse {
				if got.Refuse != "read-only connection: cannot cancel or disconnect sessions" {
					t.Fatalf("Refuse = %q", got.Refuse)
				}
				return
			}
			if got.Refuse != "" || got.Title != c.title || got.Expect != c.expect {
				t.Fatalf("got %+v", got)
			}
			for _, l := range c.lines {
				if !slices.Contains(got.Body, l) {
					t.Errorf("body lacks %q: %q", l, got.Body)
				}
			}
			for _, l := range c.absent {
				if slices.Contains(got.Body, l) {
					t.Errorf("body has %q", l)
				}
			}
		})
	}

	long := target
	long.SQL = strings.Repeat("x", 200)
	for _, l := range guardSession(dev, actCancel, long, 60).Body {
		if w := ansi.StringWidth(l); w > 60 {
			t.Errorf("line of width %d: %q", w, l)
		}
	}
}
