package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

// startRestore opens repo1/s1's tab and presses R.
func startRestore(t *testing.T, cfg config.Connection) (*Model, *fakeES) {
	t.Helper()
	m, _, f := newESModel(t, cfg)
	fakeSnapshots(f)
	pressAll(t, m, "l")
	openSnapshotTab(t, m, "repo1", "s1")
	pressAll(t, m, "R")
	if _, ok := m.overlay.(*overlay.RestoreForm); !ok {
		t.Fatalf("overlay %T status %q", m.overlay, m.status)
	}
	return m, f
}

func restoreWith(t *testing.T, m *Model, msg overlay.RestoreFormMsg) {
	t.Helper()
	m.overlay = nil
	drain(t, m, func() tea.Msg { return msg })
}

func TestESRestore(t *testing.T) {
	m, f := startRestore(t, config.Connection{})
	restoreWith(t, m, overlay.RestoreFormMsg{RenamePattern: "(.+)", RenameReplacement: "restored-$1"})
	v := confirmText(t, m)
	for _, want := range []string{"restore 2 indices from repo1/s1", "orders → restored-orders", "users → restored-users", "aliases: no", "global state: no", "type s1 to confirm"} {
		if !strings.Contains(v, want) {
			t.Errorf("confirm lacks %q:\n%s", want, v)
		}
	}
	typeText(t, m, "s1")
	pressAll(t, m, "enter")
	if !slices.Contains(f.sent(), "POST _snapshot/repo1/s1/_restore?wait_for_completion=false") {
		t.Fatalf("sent %v", f.sent())
	}
	if b := f.lastBody("POST _snapshot/repo1/s1/_restore"); !sameJSONText(b,
		`{"indices":"orders,users","include_global_state":false,"include_aliases":false,"rename_pattern":"(.+)","rename_replacement":"restored-$1"}`) {
		t.Errorf("body %s", b)
	}
	if !strings.Contains(m.status, "restore started: 2 indices") || !strings.Contains(m.status, "Shards to watch") {
		t.Errorf("status %q", m.status)
	}
}

func TestESRestoreOpenTarget(t *testing.T) {
	m, f := startRestore(t, config.Connection{})
	f.addIndex("orders", "", 1)
	f.addIndex("users", "", 1)
	restoreWith(t, m, overlay.RestoreFormMsg{})
	if m.overlay != nil || !strings.Contains(m.status, "orders, users exist and are open: close or delete them, or restore under a rename") {
		t.Errorf("overlay %T status %q", m.overlay, m.status)
	}
	if len(sentContaining(f, "_restore")) != 0 {
		t.Errorf("sent %v", f.sent())
	}
}

func TestESRestoreClosedTarget(t *testing.T) {
	m, f := startRestore(t, config.Connection{})
	f.addIndex("orders", "", 1).info.Status = "close"
	restoreWith(t, m, overlay.RestoreFormMsg{Indices: "orders"})
	if v := confirmText(t, m); !strings.Contains(v, "orders → orders (closed: restored into it)") || !strings.Contains(v, "restore 1 index") {
		t.Errorf("confirm:\n%s", v)
	}
}

func TestESRestoreNoMatch(t *testing.T) {
	m, f := startRestore(t, config.Connection{})
	restoreWith(t, m, overlay.RestoreFormMsg{Indices: "carts*"})
	if m.overlay != nil || m.status != "no index of s1 matches carts*" {
		t.Errorf("overlay %T status %q", m.overlay, m.status)
	}
	if len(sentContaining(f, "_restore")) != 0 {
		t.Errorf("sent %v", f.sent())
	}
}

func TestESRestoreProd(t *testing.T) {
	no := false
	m, _ := startRestore(t, config.Connection{Env: config.EnvProd, ReadOnly: &no})
	restoreWith(t, m, overlay.RestoreFormMsg{RenamePattern: "(.+)", RenameReplacement: "r-$1"})
	if v := confirmText(t, m); !strings.Contains(v, "type s1 to confirm") {
		t.Errorf("confirm:\n%s", v)
	}
}

func TestESRestoreReadOnly(t *testing.T) {
	ro := true
	m, _, f := newESModel(t, config.Connection{ReadOnly: &ro})
	fakeSnapshots(f)
	pressAll(t, m, "l")
	openSnapshotTab(t, m, "repo1", "s1")
	pressAll(t, m, "R")
	if m.overlay != nil || m.status != readOnlyHint {
		t.Errorf("overlay %T status %q", m.overlay, m.status)
	}
}
