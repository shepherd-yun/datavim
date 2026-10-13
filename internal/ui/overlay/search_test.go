package overlay

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

func TestSearchOverlay(t *testing.T) {
	s := NewSearch("")
	if v := ansi.Strip(s.View(100, 30)); !strings.Contains(v, "@") {
		t.Fatalf("empty query shows how to search everything:\n%s", v)
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
	next, cmd := s.HandleKey(uitest.Key("ctrl+s"))
	if next != nil || cmd().(SearchPickMsg) != (SearchPickMsg{Index: 1, Structure: true}) {
		t.Fatal("ctrl+s picks the structure and closes")
	}
	s.Paste("x")
	if text, gen := s.Requery(); text != "usx" || gen != 3 {
		t.Fatalf("Requery = %q, %d", text, gen)
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
