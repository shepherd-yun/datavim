package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

func TestParseKeyName(t *testing.T) {
	for _, c := range []struct{ in, want, err string }{
		{in: "a:b", want: "a:b"},
		{in: `"a b"`, want: "a b"},
		{in: `"a\xffb"`, want: "a\xffb"},
		{in: "a b", err: `quote a name with spaces, e.g. "a b"`},
		{in: "", err: "type a key name"},
		{in: "  ", err: "type a key name"},
		{in: `"abc`, err: "unterminated quote"},
	} {
		got, err := parseKeyName(c.in)
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("parseKeyName(%q) err = %v, want %q", c.in, err, c.err)
		case c.err == "" && (err != nil || got != c.want):
			t.Errorf("parseKeyName(%q) = %q, %v", c.in, got, err)
		}
	}
}

// keyOpModel opens key k (a hash) on a connection of env, writable.
func keyOpModel(t *testing.T, env config.Env, keys ...string) (*Model, *fakeRedis) {
	t.Helper()
	m, b, r := newRedisModel(t, config.Connection{Env: env})
	_ = b.SetReadOnly("cache", false)
	for _, k := range keys {
		r.put(0, k, testHash())
	}
	drainAll(t, m, m.openKey("cache", 0, keys[0]))
	return m, r
}

func calls(r *fakeRedis, prefix string) []string {
	var out []string
	for _, c := range r.keyCalls() {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func answer(t *testing.T, m *Model, text string) {
	t.Helper()
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, m.status)
	}
	pressAll(t, m, "ctrl+u")
	typeText(t, m, text)
	pressAll(t, m, "enter")
}

func TestSetTTL(t *testing.T) {
	m, r := keyOpModel(t, config.EnvDev, "k")
	pressAll(t, m, "t")
	answer(t, m, "50")
	if got := calls(r, "Expire"); !slices.Equal(got, []string{"Expire k 50s"}) || m.status != "TTL set to 50s" {
		t.Fatalf("calls %q, status %q", got, m.status)
	}
	if !strings.Contains(screen(m), "TTL 50s") {
		t.Fatalf("the tab reloads:\n%s", screen(m))
	}
	pressAll(t, m, "t")
	answer(t, m, "-1")
	if got := calls(r, "Expire"); len(got) != 2 || got[1] != "Expire k -1ns" || m.status != "TTL removed" {
		t.Fatalf("calls %q, status %q", got, m.status)
	}
	pressAll(t, m, "t")
	answer(t, m, "abc")
	if _, ok := m.overlay.(*overlay.Prompt); !ok || !strings.Contains(screen(m), "type seconds (a whole number), or -1 to remove the TTL") {
		t.Fatalf("overlay %T:\n%s", m.overlay, screen(m))
	}
}

func TestSetTTLProd(t *testing.T) {
	m, r := keyOpModel(t, config.EnvProd, "k")
	pressAll(t, m, "t")
	answer(t, m, "50")
	if _, ok := m.overlay.(*overlay.Confirm); !ok || len(calls(r, "Expire")) != 0 {
		t.Fatalf("overlay %T, calls %q", m.overlay, calls(r, "Expire"))
	}
	pressAll(t, m, "y")
	if got := calls(r, "Expire"); len(got) != 1 {
		t.Fatalf("calls %q", got)
	}
}

func TestRenameKeyBinary(t *testing.T) {
	m, r := keyOpModel(t, config.EnvDev, "k")
	pressAll(t, m, "r")
	answer(t, m, `"a\xffb"`)
	if got := calls(r, "Rename"); !slices.Equal(got, []string{"Rename k a\xffb false"}) {
		t.Fatalf("calls %q, status %q", got, m.status)
	}
	if m.status != `renamed k to a\xFFb` {
		t.Fatalf("status %q", m.status)
	}
}

func TestRenameKeyOverwrite(t *testing.T) {
	for _, env := range []config.Env{config.EnvDev, config.EnvProd} {
		m, r := keyOpModel(t, env, "k", "taken")
		pressAll(t, m, "r")
		answer(t, m, "taken")
		if env == config.EnvProd {
			pressAll(t, m, "y") // the write to prod
		}
		c, ok := m.overlay.(*overlay.Confirm)
		if !ok || !strings.Contains(screen(m), "taken already exists · overwrite it?") {
			t.Fatalf("%s: overlay %T:\n%s", env, m.overlay, screen(m))
		}
		_ = c
		if env == config.EnvProd {
			pressAll(t, m, "y")
			if got := calls(r, "Rename"); len(got) != 1 {
				t.Fatalf("prod: y alone renamed: %q", got)
			}
			pressAll(t, m, "ctrl+u")
			typeText(t, m, "cache")
			pressAll(t, m, "enter")
		} else {
			pressAll(t, m, "y")
		}
		if got := calls(r, "Rename"); len(got) != 2 || got[1] != "Rename k taken true" {
			t.Fatalf("%s: calls %q", env, got)
		}
	}
}

func TestRenameKeyRetargetsTab(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.put(0, "k", testHash())
	db0 := expandDB0(t, m)
	drainAll(t, m, m.openKey("cache", 0, "k"))
	pressAll(t, m, "r")
	answer(t, m, "n")
	kt := m.activeTab().(*keyTab)
	if kt.Title() != "n" || kt.Key() != keyTabKey("cache", 0, "n") || !kt.info.Exists {
		t.Fatalf("tab %q %q %+v", kt.Title(), kt.Key(), kt.info)
	}
	if countKeys(db0) != 1 || db0.Children[0].Key != "n" {
		t.Fatalf("tree:\n%s", screen(m))
	}
}

func TestRenameKeyRefusedWithChanges(t *testing.T) {
	m, _ := keyOpModel(t, config.EnvDev, "k")
	pressAll(t, m, "l")
	editTo(t, m, "9")
	for _, k := range []string{"r", "D", "t"} {
		pressAll(t, m, k)
		if m.overlay != nil || !strings.HasPrefix(m.status, "1 unsaved change") {
			t.Fatalf("%s: overlay %T, status %q", k, m.overlay, m.status)
		}
	}
}

func TestDeleteKeyFromTree(t *testing.T) {
	for _, env := range []config.Env{config.EnvDev, config.EnvProd} {
		m, b, r := newRedisModel(t, config.Connection{Env: env})
		_ = b.SetReadOnly("cache", false)
		r.put(0, "k", testHash())
		r.put(0, "other", testHash())
		db0 := expandDB0(t, m)
		pressAll(t, m, "j", "d", "d")
		if _, ok := m.overlay.(*overlay.Confirm); !ok || !strings.Contains(screen(m), "Delete k (hash)?") {
			t.Fatalf("%s: overlay %T:\n%s", env, m.overlay, screen(m))
		}
		pressAll(t, m, "y")
		if env == config.EnvProd {
			if len(r.unlinks) != 0 {
				t.Fatal("prod: y alone deleted the key")
			}
			pressAll(t, m, "ctrl+u")
			typeText(t, m, "cache")
			pressAll(t, m, "enter")
		}
		if len(r.unlinks) != 1 || r.unlinks[0][0] != "k" || m.status != "deleted k" {
			t.Fatalf("%s: unlinks %q, status %q", env, r.unlinks, m.status)
		}
		if countKeys(db0) != 1 {
			t.Fatalf("%s: tree:\n%s", env, screen(m))
		}
	}
}

func TestDeleteKeyFromTab(t *testing.T) {
	m, r := keyOpModel(t, config.EnvDev, "k")
	pressAll(t, m, "D", "y")
	if len(r.unlinks) != 1 || !strings.Contains(screen(m), "key no longer exists") {
		t.Fatalf("unlinks %q:\n%s", r.unlinks, screen(m))
	}
}

func TestKeyOpsReadOnly(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{Env: config.EnvProd}) // read-only by default
	r.put(0, "k", testHash())
	expandDB0(t, m)
	pressAll(t, m, "j")
	if n := m.tree.Selected(); n.Kind != tree.KindRedisKey {
		t.Fatalf("selected %+v", n)
	}
	for _, k := range [][]string{{"r"}, {"d", "d"}} {
		pressAll(t, m, k...)
		if m.overlay != nil || m.status != readOnlyHint {
			t.Fatalf("tree %v: overlay %T, status %q", k, m.overlay, m.status)
		}
	}
	drainAll(t, m, m.openKey("cache", 0, "k"))
	for _, k := range []string{"t", "r", "D"} {
		m.status = ""
		pressAll(t, m, k)
		if m.overlay != nil || m.status != readOnlyHint {
			t.Fatalf("tab %s: overlay %T, status %q", k, m.overlay, m.status)
		}
	}
}
