package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

func TestBulkPattern(t *testing.T) {
	ns := db.Namespace{Database: "0"}
	for _, c := range []struct {
		n      *tree.Node
		filter string
		want   string
	}{
		{&tree.Node{Kind: tree.KindKeyFolder, Key: "del:", NS: ns}, "", "del:*"},
		{&tree.Node{Kind: tree.KindKeyFolder, Key: "a*b:", NS: ns}, "x*", `a\*b:*`},
		{&tree.Node{Kind: tree.KindRedisDB, NS: ns}, "user:*", "user:*"},
		{&tree.Node{Kind: tree.KindRedisDB, NS: ns}, "", "*"},
	} {
		if got := bulkPattern(c.n, c.filter); got != c.want {
			t.Errorf("bulkPattern(%v %q, %q) = %q", c.n.Kind, c.n.Key, c.filter, got)
		}
	}
}

// bulkModel has del:0 … del:2499 and keep:0 … keep:9 in db0, which is
// expanded; the del: folder is selected.
func bulkModel(t *testing.T, env config.Env) (*Model, *fakeRedis) {
	t.Helper()
	m, b, r := newRedisModel(t, config.Connection{Env: env})
	_ = b.SetReadOnly("cache", false)
	r.fill(0, "del:", 2500)
	r.fill(0, "keep:", 10)
	expandDB0(t, m)
	pressAll(t, m, "j")
	if n := m.tree.Selected(); n.Kind != tree.KindKeyFolder || n.Key != "del:" {
		t.Fatalf("selected %+v\n%s", n, screen(m))
	}
	return m, r
}

func dbKeys(r *fakeRedis) (del, keep int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.dbs[0] {
		if strings.HasPrefix(k, "del:") {
			del++
		} else {
			keep++
		}
	}
	return del, keep
}

func TestBulkDeleteFolder(t *testing.T) {
	m, r := bulkModel(t, config.EnvDev)
	pressAll(t, m, "d", "d")
	if _, ok := m.overlay.(*overlay.Confirm); !ok {
		t.Fatalf("overlay %T, status %q", m.overlay, m.status)
	}
	s := screen(m)
	for _, want := range []string{"Delete 2,500 keys matching del:* in cache/db0?", "del:0", "… and 2,480 more", "Keys written while deleting may be missed."} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	pressAll(t, m, "y")
	if del, keep := dbKeys(r); del != 0 || keep != 10 {
		t.Fatalf("left del %d keep %d", del, keep)
	}
	if m.status != "deleted 2,500 keys" {
		t.Fatalf("status %q", m.status)
	}
	for _, u := range r.unlinks {
		if len(u) > bulkUnlink {
			t.Fatalf("an UNLINK of %d keys", len(u))
		}
	}
	if m.bulk != nil {
		t.Fatal("the bulk delete is over")
	}
}

func TestBulkDeleteProd(t *testing.T) {
	m, r := bulkModel(t, config.EnvProd)
	pressAll(t, m, "d", "d", "y", "enter")
	if del, _ := dbKeys(r); del != 2500 {
		t.Fatalf("y deleted %d keys on prod", 2500-del)
	}
	if _, ok := m.overlay.(*overlay.Confirm); !ok {
		t.Fatalf("overlay %T", m.overlay)
	}
	pressAll(t, m, "ctrl+u")
	typeText(t, m, "cache")
	pressAll(t, m, "enter")
	if del, _ := dbKeys(r); del != 0 {
		t.Fatalf("left %d", del)
	}
}

// stepOnce runs cmd and gives its message to m; it returns what m wants
// to run next.
func stepOnce(t *testing.T, m *Model, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("nothing to run")
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var next []tea.Cmd
		for _, c := range msg {
			if c != nil {
				next = append(next, stepOnce(t, m, c))
			}
		}
		return tea.Batch(next...)
	case nil:
		return nil
	default:
		_, next := m.Update(msg)
		return next
	}
}

func TestBulkDeleteCancel(t *testing.T) {
	m, r := bulkModel(t, config.EnvDev)
	pressAll(t, m, "d", "d")
	_, cmd := m.Update(uitest.Key("y"))
	cmd = stepOnce(t, m, cmd) // the confirmation starts the delete
	cmd = stepOnce(t, m, cmd) // the first batch
	first, _ := dbKeys(r)
	first = 2500 - first
	if first == 0 || m.bulk == nil {
		t.Fatalf("first batch deleted %d", first)
	}
	scans := len(r.scanCalls())
	pressAll(t, m, "ctrl+c")
	if want := "stopped · deleted " + groupDigits(int64(first)) + " keys"; m.status != want {
		t.Fatalf("status %q, want %q", m.status, want)
	}
	if len(r.scanCalls()) == scans {
		t.Fatal("the db is scanned again after stopping")
	}
	drainAll(t, m, cmd) // the batch already on its way is dropped
	if del, _ := dbKeys(r); 2500-del != first {
		t.Fatalf("deleted %d after stopping at %d", 2500-del, first)
	}
	if m.bulk != nil {
		t.Fatal("stopped")
	}
}

func TestBulkDeleteNoMatch(t *testing.T) {
	m, b, r := newRedisModel(t, config.Connection{})
	_ = b.SetReadOnly("cache", false)
	r.fill(0, "keep:", 3)
	expandDB0(t, m)
	drainAll(t, m, m.filterKeys(redisDBKey{conn: "cache"}, "nope*"))
	pressAll(t, m, "d", "d")
	if m.overlay != nil || m.status != "no keys match nope*" {
		t.Fatalf("overlay %T, status %q", m.overlay, m.status)
	}
}

func TestBulkDeleteReadOnly(t *testing.T) {
	m, b, r := newRedisModel(t, config.Connection{})
	_ = b.SetReadOnly("cache", true)
	r.fill(0, "del:", 3)
	expandDB0(t, m)
	scans := len(r.scanCalls())
	pressAll(t, m, "d", "d")
	if m.status != readOnlyHint || len(r.scanCalls()) != scans {
		t.Fatalf("status %q, scans %d → %d", m.status, scans, len(r.scanCalls()))
	}
}
