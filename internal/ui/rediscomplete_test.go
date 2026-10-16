package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
)

// typeR types s into the console in INSERT mode.
func typeR(t *testing.T, m *Model, c *redisConsoleTab, s string) {
	t.Helper()
	if !c.editor.Insert() {
		pressAll(t, m, "i")
	}
	for _, r := range s {
		k := string(r)
		if r == ' ' {
			k = "space"
		}
		pressAll(t, m, k)
	}
}

// appendR puts text in the editor and appends at its end in INSERT mode
// without typing it (no completion runs for it).
func appendR(t *testing.T, m *Model, c *redisConsoleTab, text string) {
	t.Helper()
	c.editor.SetValue(text)
	c.editor.Bottom()
	pressAll(t, m, "A")
}

func TestRedisCompleteCommand(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	typeR(t, m, c, "hge")
	if c.popup == nil || c.popup.Labels()[0] != "HGET" {
		t.Fatalf("popup = %+v", c.popup)
	}
	if !strings.Contains(screen(m), "key field") {
		t.Fatalf("no argument hint in the popup:\n%s", screen(m))
	}
	pressAll(t, m, "enter")
	if got := c.editor.Value(); got != "HGET " {
		t.Fatalf("editor = %q", got)
	}
	if !strings.Contains(screen(m), "HGET key field") {
		t.Fatalf("hint not shown:\n%s", screen(m))
	}
}

func TestRedisCompleteSubcommand(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	typeR(t, m, c, "CONFIG res")
	if c.popup == nil || !slices.Contains(c.popup.Labels(), "RESETSTAT") {
		t.Fatalf("popup = %+v", c.popup)
	}
}

func TestRedisCompleteKey(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	for _, k := range []string{"user:1", "user:2", "a b"} {
		r.put(0, k, fakeValue{typ: "string", str: "v"})
	}
	expandDB0(t, m)
	c := openRConsole(t, m, 0)
	typeR(t, m, c, "GET us")
	if c.popup == nil || !slices.Equal(c.popup.Labels(), []string{"user:1", "user:2"}) {
		t.Fatalf("popup = %+v", c.popup)
	}
	pressAll(t, m, "esc", "esc")
	c.editor.SetValue("")
	typeR(t, m, c, "GET a")
	if c.popup == nil {
		t.Fatal("no popup for a")
	}
	pressAll(t, m, "enter")
	if got := c.editor.Value(); got != `GET "a b"` {
		t.Fatalf("editor = %q", got)
	}
}

func TestRedisCompleteBackfill(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.fill(0, "order:", 3)
	c := openRConsole(t, m, 0)
	appendR(t, m, c, "GET or")
	typeR(t, m, c, "d")
	scans := r.scanCalls()
	if len(scans) == 0 || scans[len(scans)-1].match != "ord*" {
		t.Fatalf("scans = %+v", scans)
	}
	if c.popup == nil || c.popup.Len() != 3 {
		t.Fatalf("popup = %+v", c.popup)
	}
	n := len(r.scanCalls())
	typeR(t, m, c, "e")
	if got := len(r.scanCalls()); got != n {
		t.Fatalf("scanned again for orde: %+v", r.scanCalls()[n:])
	}
	if c.popup == nil || c.popup.Len() != 3 {
		t.Fatalf("popup after e = %+v", c.popup)
	}
}

func TestRedisCompleteGlobPrefix(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	appendR(t, m, c, "GET a")
	typeR(t, m, c, "*")
	scans := r.scanCalls()
	if len(scans) == 0 || scans[len(scans)-1].match != `a\**` {
		t.Fatalf("scans = %+v", scans)
	}
}

func TestRedisCompleteNoKeyForField(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	appendR(t, m, c, "HSET user:1")
	typeR(t, m, c, " f")
	if c.popup != nil || len(r.scanCalls()) != 0 {
		t.Fatalf("popup %+v, scans %+v", c.popup, r.scanCalls())
	}
}

func TestRedisConsoleHighlights(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	c := openRConsole(t, m, 0)
	c.editor.SetValue("SET a 1")
	plain := c.editor.View()
	c.editor.SetHighlighter(nil)
	if c.editor.View() == plain {
		t.Fatal("the console does not highlight")
	}
}
