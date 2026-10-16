package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
)

func commaModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	m, b := newTestModel(t)
	k, err := keymap.Custom(",", nil)
	if err != nil {
		t.Fatal(err)
	}
	m.SetKeymap(k)
	return m, b
}

func TestHintsFollowCustomLeader(t *testing.T) {
	m, _ := commaModel(t)
	s := screen(m)
	if strings.Contains(s, "<space>") {
		t.Fatalf("screen still mentions <space>:\n%s", s)
	}
	// welcomeText pads keys to 10 columns plus a space, as "<space>c   " did.
	for _, want := range []string{",c         console (SQL or Redis)", ", leader"} {
		if !strings.Contains(s, want) {
			t.Fatalf("screen lacks %q:\n%s", want, s)
		}
	}

	openUsers(t, m)
	press(t, m, "o") // stage a new row
	if s := screen(m); !strings.Contains(s, ",w submit") || strings.Contains(s, "<space>w") {
		t.Fatalf("data tab hint:\n%s", s)
	}
	press(t, m, "]")
	if s := screen(m); !strings.Contains(s, ",w submits") {
		t.Fatalf("unsaved-changes status:\n%s", s)
	}
}

func TestConsoleHintFollowsKeymap(t *testing.T) {
	m, _ := commaModel(t)
	press(t, m, "l", "j", ",", "c")
	if s := screen(m); !strings.Contains(s, ",v $EDITOR") || strings.Contains(s, "<space>v") {
		t.Fatalf("console hint:\n%s", s)
	}
}

func TestConsoleLeaderIsNotTakenByEditor(t *testing.T) {
	m, _ := commaModel(t)
	press(t, m, "l", "j", ",", "c", "esc") // console opens focused, INSERT mode
	press(t, m, ",")
	if len(m.pending) != 1 || m.pending[0] != "," {
		t.Fatalf("pending = %q, want the leader", m.pending)
	}
	press(t, m, "esc")
	press(t, m, "f", ",") // "f," finds a comma: the editor owns the key mid-command
	if len(m.pending) != 0 {
		t.Fatalf("pending = %q, want the editor to take , after f", m.pending)
	}
}

func TestKeyHintOmitsUnboundActions(t *testing.T) {
	k, err := keymap.Custom("", map[keymap.Context]map[string]string{keymap.Console: {"<leader>v": ""}})
	if err != nil {
		t.Fatal(err)
	}
	if got := keyHint(k, keymap.Console, keymap.ExternalEdit, "$EDITOR"); got != "" {
		t.Fatalf("keyHint = %q, want empty for an unbound action", got)
	}
	if got := joinHints("a", "", "b"); got != "a · b" {
		t.Fatalf("joinHints = %q", got)
	}
}

func TestConsoleLeaderIsTextInInsertMode(t *testing.T) {
	m, _ := commaModel(t)
	press(t, m, "l", "j", ",", "c", "a", ",", "b") // console opens in INSERT mode
	if got := m.activeTab().(*consoleTab).editor.Value(); got != "a,b" {
		t.Fatalf("editor = %q, want the leader typed as text", got)
	}
}

func TestSearchNoteOmitsAnUnboundRetry(t *testing.T) {
	m, b := newTestModel(t)
	k, err := keymap.Custom("", map[keymap.Context]map[string]string{keymap.Global: {"<leader>r": ""}})
	if err != nil {
		t.Fatal(err)
	}
	m.SetKeymap(k)
	b.conn.mu.Lock()
	b.conn.catalogErr = errors.New("permission denied")
	b.conn.mu.Unlock()
	press(t, m, "l")
	search(t, m, "")
	if got := m.searchNote(); got != "metadata of local failed: permission denied · metadata of 1 connection failed" {
		t.Fatalf("searchNote = %q, want no dangling separator", got)
	}
}

func TestUnsavedHintOmitsUnboundKeys(t *testing.T) {
	m, _ := newTestModel(t)
	k, err := keymap.Custom("", map[keymap.Context]map[string]string{keymap.Grid: {"<leader>w": "", "U": ""}})
	if err != nil {
		t.Fatal(err)
	}
	m.SetKeymap(k)
	openUsers(t, m)
	press(t, m, "o", "]")
	if s := screen(m); !strings.Contains(s, "1 unsaved change ") || strings.Contains(s, "1 unsaved change · ") {
		t.Fatalf("status keeps a dangling separator:\n%s", s)
	}
}

func TestWelcomeNamesTheOpenKey(t *testing.T) {
	m, _ := newTestModel(t)
	s := screen(m)
	if !strings.Contains(s, "press <enter> to open it") || strings.Contains(s, "l or enter") {
		t.Fatalf("welcome text must name tree.open's keys, not l:\n%s", s)
	}
}
