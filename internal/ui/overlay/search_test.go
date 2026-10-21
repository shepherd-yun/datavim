package overlay

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/uitest"
)

func TestSearchOverlay(t *testing.T) {
	if v := ansi.Strip(NewSearch(keymap.Default(), "", "").View(100, 30)); strings.Contains(v, "@") {
		t.Fatalf("no connection in context, nothing to narrow to:\n%s", v)
	}
	s := NewSearch(keymap.Default(), "", "local")
	if v := ansi.Strip(s.View(100, 30)); !strings.Contains(v, "@ to search only local") {
		t.Fatalf("empty query shows how to narrow the search:\n%s", v)
	}
	_, cmd := s.HandleKey(uitest.Key("u"))
	if q := cmd().(SearchQueryMsg); q.Text != "u" || q.Gen != 1 {
		t.Fatalf("query = %+v", q)
	}
	_, cmd = s.HandleKey(uitest.Key("s"))
	if q := cmd().(SearchQueryMsg); q.Text != "us" || q.Gen != 2 {
		t.Fatalf("query = %+v", q)
	}
	if v := ansi.Strip(s.View(100, 30)); !strings.Contains(v, "searching…") {
		t.Fatalf("waiting for results:\n%s", v)
	}
	items := []SearchItem{{Kind: "table", Label: "users", Where: "app"}, {Kind: "column", Label: "users.email", Where: "app · text"}}
	if s.SetResults(1, items) {
		t.Fatal("results of an older query are dropped")
	}
	if !s.SetResults(2, items) {
		t.Fatal("current results are shown")
	}
	v := ansi.Strip(s.View(100, 30))
	for _, want := range []string{"table    users  app", "users.email", "2 matches"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if _, cmd := s.HandleKey(uitest.Key("enter")); cmd().(SearchPickMsg) != (SearchPickMsg{Index: 0}) {
		t.Fatal("enter picks the first row")
	}
	s.HandleKey(uitest.Key("down"))
	if v := ansi.Strip(s.View(100, 30)); !strings.Contains(v, "<ctrl+o> structure") {
		t.Fatalf("footer:\n%s", v)
	}
	next, cmd := s.HandleKey(uitest.Key("ctrl+o"))
	if next != nil || cmd().(SearchPickMsg) != (SearchPickMsg{Index: 1, Structure: true}) {
		t.Fatal("ctrl+o picks the structure and closes")
	}
	s.Paste("x")
	if q := s.Requery(); q.Text != "usx" || q.Gen != 3 || q.Scope != ScopeObjects {
		t.Fatalf("Requery = %+v", q)
	}
	if next, _ := s.HandleKey(uitest.Key("esc")); next != nil {
		t.Fatal("esc closes")
	}
	s.SetNote("loading metadata of local…")
	if v := ansi.Strip(s.View(100, 30)); !strings.Contains(v, "loading metadata of local…") {
		t.Fatalf("note:\n%s", v)
	}
	for _, l := range strings.Split(ansi.Strip(s.View(40, 10)), "\n") {
		if ansi.StringWidth(l) > 36 {
			t.Fatalf("too wide: %q", l)
		}
	}
}

func TestSearchOverlaySwitchesScopes(t *testing.T) {
	s := NewSearch(keymap.Default(), "", "local")
	if s.Scope() != ScopeObjects {
		t.Fatalf("opens on %v", s.Scope())
	}
	if v := ansi.Strip(s.View(100, 30)); !strings.Contains(v, "objects  consoles  history") || !strings.Contains(v, "<tab> next scope") {
		t.Fatalf("scope bar and its key:\n%s", v)
	}
	s.HandleKey(uitest.Key("u"))
	_, cmd := s.HandleKey(uitest.Key("tab"))
	if q := cmd().(SearchQueryMsg); q.Scope != ScopeConsoles || q.Text != "u" || q.Gen != 2 {
		t.Fatalf("tab requeries the text in the next scope: %+v", q)
	}
	if v := ansi.Strip(s.View(100, 30)); !strings.Contains(v, "searching…") {
		t.Fatalf("waiting for the console results:\n%s", v)
	}
	s.SetResults(2, []SearchItem{{Kind: "console", Label: "select 1", Where: "console:app · line 3"}})
	if next, cmd := s.HandleKey(uitest.Key("ctrl+o")); next == nil || cmd != nil {
		t.Fatal("the structure key does nothing outside objects")
	}
	_, cmd = s.HandleKey(uitest.Key("tab"))
	if q := cmd().(SearchQueryMsg); q.Scope != ScopeHistory {
		t.Fatalf("history after consoles: %+v", q)
	}
	_, cmd = s.HandleKey(uitest.Key("tab"))
	if q := cmd().(SearchQueryMsg); q.Scope != ScopeObjects {
		t.Fatalf("tab wraps around: %+v", q)
	}
	_, cmd = s.HandleKey(uitest.Key("shift+tab"))
	if q := cmd().(SearchQueryMsg); q.Scope != ScopeHistory {
		t.Fatalf("shift+tab goes back: %+v", q)
	}
	s.HandleKey(uitest.Key("backspace"))
	q := s.Requery()
	if q.Text != "" || q.Scope != ScopeHistory {
		t.Fatalf("Requery = %+v", q)
	}
	s.SetResults(q.Gen, []SearchItem{{Kind: "history", Label: "select 1"}, {Kind: "history", Label: "select 2"}})
	if v := ansi.Strip(s.View(100, 30)); !strings.Contains(v, "2 recent statements") {
		t.Fatalf("an empty history query lists the recent statements:\n%s", v)
	}
}
