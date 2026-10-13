package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

const updateUser1 = "UPDATE `app`.`users` SET `name` = 'user1x' WHERE `id` = '1'"

// stageUser1 renames user1 to user1x in the open users tab.
func stageUser1(t *testing.T, m *Model) {
	t.Helper()
	press(t, m, "l", "i", "x", "enter")
}

func TestSubmitPreviewAndApply(t *testing.T) {
	m, b, d := openUsersTab(t)
	stageUser1(t, m)
	press(t, m, "space", "w")
	if _, ok := m.overlay.(*overlay.Preview); !ok {
		t.Fatalf("<space>w previews:\n%s", screen(m))
	}
	if s := screen(m); !strings.Contains(s, "Submit 1 change to users") || !strings.Contains(s, updateUser1+";") {
		t.Fatalf("preview:\n%s", s)
	}
	if len(b.conn.execList()) != 0 {
		t.Fatal("nothing runs before y")
	}
	press(t, m, "y")
	if got, want := b.conn.execList(), []string{"START TRANSACTION", updateUser1, "COMMIT"}; !slices.Equal(got, want) {
		t.Fatalf("execs = %q, want %q", got, want)
	}
	if d.changes() != 0 || d.submitting || !strings.Contains(screen(m), "submitted 1 change") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	if !strings.HasSuffix(b.conn.lastQuery(), "LIMIT 3 OFFSET 0") {
		t.Fatalf("the page is reloaded: %q", b.conn.lastQuery())
	}
	st := m.store.(*fakeStore)
	if len(st.entries) != 1 || st.entries[0].SQL != updateUser1+";" || st.entries[0].Err != "" {
		t.Fatalf("history = %+v", st.entries)
	}
}

func TestWriteCommand(t *testing.T) {
	m, _, _ := openUsersTab(t)
	command(t, m, "w")
	if !strings.Contains(screen(m), "no changes to submit") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	stageUser1(t, m)
	command(t, m, "w")
	if _, ok := m.overlay.(*overlay.Preview); !ok {
		t.Fatalf(":w previews:\n%s", screen(m))
	}

	c, _ := newTestModel(t)
	openLocalConsole(t, c)
	command(t, c, "w")
	if !strings.Contains(statusLine(c), "nothing to write") {
		t.Fatalf("status: %s", statusLine(c))
	}
}

func TestSubmitFailureKeepsChanges(t *testing.T) {
	m, b, d := openUsersTab(t)
	b.conn.setAffected(updateUser1, 0) // someone deleted the row meanwhile
	stageUser1(t, m)
	press(t, m, "space", "w", "y")
	if got := b.conn.execList(); got[len(got)-1] != "ROLLBACK" {
		t.Fatalf("execs = %q", got)
	}
	s := screen(m)
	if !strings.Contains(s, "changes kept") || !strings.Contains(s, "changed 0 rows, expected 1") {
		t.Fatalf("screen:\n%s", s)
	}
	if d.changes() != 1 || d.submitting {
		t.Fatal("the change is kept for another try")
	}
	if st := m.store.(*fakeStore); len(st.entries) != 1 || st.entries[0].Err == "" {
		t.Fatalf("the failure is in the history: %+v", st.entries)
	}
}

func TestSubmitOnProd(t *testing.T) {
	m, b := newTestModel(t)
	openProdUsers(t, m)
	command(t, m, "set noreadonly")
	press(t, m, "l", "i", "x", "enter", "space", "w")
	if !strings.Contains(screen(m), "PROD · Submit 1 change to users") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	press(t, m, "n")
	if m.overlay != nil || len(b.conn.execList()) != 0 {
		t.Fatalf("n cancels: %q", b.conn.execList())
	}
	if d := m.activeTab().(*dataTab); d.changes() != 1 {
		t.Fatal("the change stays staged")
	}
}

func TestReadOnlyRejectedAtSubmit(t *testing.T) {
	m, b, _ := openUsersTab(t)
	stageUser1(t, m)
	command(t, m, "set readonly")
	press(t, m, "space", "w")
	if m.overlay != nil || len(b.conn.execList()) != 0 || !strings.Contains(screen(m), "read-only connection") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestCloseAndQuitAskWithUnsavedChanges(t *testing.T) {
	m, _, _ := openUsersTab(t)
	stageUser1(t, m)
	command(t, m, "q")
	if len(m.tabs) != 1 || !strings.Contains(screen(m), "Unsaved changes") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	press(t, m, "n")
	if command(t, m, "qa") {
		t.Fatal(":qa asks first")
	}
	if s := screen(m); !strings.Contains(s, "Unsaved changes in: users (1)") {
		t.Fatalf("screen:\n%s", s)
	}
	press(t, m, "n")
	command(t, m, "q!")
	if len(m.tabs) != 0 {
		t.Fatal(":q! closes without asking")
	}
}

func TestEditConnectionBlockedByUnsavedChanges(t *testing.T) {
	m, _, _ := openUsersTab(t)
	stageUser1(t, m)
	press(t, m, "ctrl+h", "g", "g", "e")
	if strings.Contains(screen(m), "Edit connection") || !strings.Contains(statusLine(m), "unsaved changes") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}
