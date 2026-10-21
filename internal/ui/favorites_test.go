package ui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/history"
	"github.com/shepherd-yun/datavim/internal/rcmd"
)

func storedFavorites(t *testing.T, m *Model) []history.Favorite {
	t.Helper()
	fs, err := m.store.Favorites()
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestToggleFavorite(t *testing.T) {
	m, _ := openTestKey(t, "user:1", fakeValue{typ: "string", str: "v"})
	pressAll(t, m, "*")
	fs := storedFavorites(t, m)
	if len(fs) != 1 || fs[0].Conn != "cache" || fs[0].Database != "0" || fs[0].Name != "user:1" {
		t.Fatalf("favorites = %+v", fs)
	}
	if h := strings.Split(m.activeTab().View(true), "\n")[0]; !strings.HasPrefix(h, "★ ") {
		t.Fatalf("header = %q", h)
	}
	if !strings.Contains(m.status, "starred user:1") {
		t.Fatalf("status = %q", m.status)
	}
	pressAll(t, m, "*")
	if fs := storedFavorites(t, m); len(fs) != 0 {
		t.Fatalf("after unstar: %+v", fs)
	}
	if h := strings.Split(m.activeTab().View(true), "\n")[0]; strings.HasPrefix(h, "★") {
		t.Fatalf("header = %q", h)
	}
	if !strings.Contains(m.status, "unstarred user:1") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestFavoriteBinaryKey(t *testing.T) {
	const key = "a\xffb\x00"
	m, _ := openTestKey(t, key, fakeValue{typ: "string", str: "v"})
	pressAll(t, m, "*")
	fs := storedFavorites(t, m)
	if len(fs) != 1 || fs[0].Name != rcmd.Quote(key) || !utf8.ValidString(fs[0].Name) {
		t.Fatalf("favorites = %+v", fs)
	}
	// close it, then open it again from the starred list
	m.Update(closeTabConfirmedMsg{tabID: m.activeTab().ID()})
	pressAll(t, m, "space", "F")
	if s := screen(m); !strings.Contains(s, "Starred keys") || !strings.Contains(s, `cache/db0  a\xFFb\x00`) {
		t.Fatalf("picker:\n%s", s)
	}
	pressAll(t, m, "enter")
	kt, ok := m.activeTab().(*keyTab)
	if !ok || kt.key != key {
		t.Fatalf("opened %T %q", m.activeTab(), kt.key)
	}
}

func TestFavoritesEmpty(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	pressAll(t, m, "space", "F")
	if m.status != "no starred keys yet · * stars one" {
		t.Fatalf("status = %q", m.status)
	}
}

func TestFavoritesLoadedAtStart(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.put(0, "k", fakeValue{typ: "string", str: "v"})
	if err := m.store.AddFavorite(history.Favorite{Conn: "cache", Database: "0", Name: "k", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	drainAll(t, m, m.Init())
	if !m.isFavorite(favKey{conn: "cache", db: 0, key: "k"}) {
		t.Fatal("not loaded")
	}
	drainAll(t, m, m.openKey("cache", 0, "k"))
	if h := strings.Split(m.activeTab().View(true), "\n")[0]; !strings.HasPrefix(h, "★ ") {
		t.Fatalf("header = %q", h)
	}
}

func TestFavoriteBadEntries(t *testing.T) {
	m, _, _ := newRedisModel(t, config.Connection{})
	_ = m.store.AddFavorite(history.Favorite{Conn: "gone", Database: "0", Name: "k", Time: time.Now()})
	pressAll(t, m, "space", "F", "enter")
	if !strings.Contains(m.status, "connection gone no longer exists") {
		t.Fatalf("status = %q", m.status)
	}
	_ = m.store.AddFavorite(history.Favorite{Conn: "cache", Database: "0", Name: `"unterminated`, Time: time.Now().Add(time.Second)})
	pressAll(t, m, "space", "F", "enter")
	if m.activeTab() != nil || !m.statusErr {
		t.Fatalf("opened %T, status %q", m.activeTab(), m.status)
	}
}

func TestTreeStar(t *testing.T) {
	m, _ := newKeyModel(t, "user:1", "solo")
	pressAll(t, m, "*")
	if m.status != "select a Redis key to star" {
		t.Fatalf("db node: status %q", m.status)
	}
	pressAll(t, m, "j", "*") // the folder user
	if m.status != "select a Redis key to star" {
		t.Fatalf("folder: status %q", m.status)
	}
	pressAll(t, m, "j", "*") // the key solo
	if fs := storedFavorites(t, m); len(fs) != 1 || fs[0].Name != "solo" {
		t.Fatalf("favorites = %+v (selected %+v)", fs, m.tree.Selected())
	}
}

func TestTreeStarNotKey(t *testing.T) {
	m, _ := newKeyModel(t, "user:1")
	pressAll(t, m, "*")
	if m.status != "select a Redis key to star" {
		t.Fatalf("status %q", m.status)
	}
}

func TestRecentKey(t *testing.T) {
	m, _ := openTestKey(t, "user:1", fakeValue{typ: "string", str: "v"})
	rs, _ := m.store.Recent(10)
	if len(rs) != 1 || rs[0].Kind != history.RecentRedisKey || rs[0].Conn != "cache" || rs[0].Database != "0" || rs[0].Name != "user:1" {
		t.Fatalf("recent = %+v", rs)
	}
	m.Update(closeTabConfirmedMsg{tabID: m.activeTab().ID()})
	pressAll(t, m, "space", "f")
	if s := screen(m); !strings.Contains(s, "key      cache/db0  user:1") {
		t.Fatalf("picker:\n%s", s)
	}
	pressAll(t, m, "enter")
	if kt, ok := m.activeTab().(*keyTab); !ok || kt.key != "user:1" {
		t.Fatalf("opened %T", m.activeTab())
	}
}

func TestRecentKeyBinary(t *testing.T) {
	const key = "a\xffb\x00"
	m, _ := openTestKey(t, key, fakeValue{typ: "string", str: "v"})
	rs, _ := m.store.Recent(10)
	if len(rs) != 1 || rs[0].Name != rcmd.Quote(key) {
		t.Fatalf("recent = %+v", rs)
	}
	m.Update(closeTabConfirmedMsg{tabID: m.activeTab().ID()})
	pressAll(t, m, "space", "f", "enter")
	if kt, ok := m.activeTab().(*keyTab); !ok || kt.key != key {
		t.Fatalf("opened %T", m.activeTab())
	}
}
