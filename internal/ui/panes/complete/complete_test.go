package complete

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func labels(cs []Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Label
	}
	return out
}

func TestRank(t *testing.T) {
	cands := []Candidate{
		{Label: "name", Kind: Column}, {Label: "nickname", Kind: Column}, {Label: "users", Kind: Table},
		{Label: "NOT", Kind: Keyword}, {Label: "NULL", Kind: Keyword},
	}
	if got := strings.Join(labels(Rank("n", cands, 10)), ","); got != "name,nickname,NOT,NULL" {
		t.Errorf("prefix matches keep their order: %s", got)
	}
	if got := labels(Rank("NA", cands, 10)); len(got) < 2 || got[0] != "name" || got[1] != "nickname" {
		t.Errorf("prefix first, then fuzzy: %v", got)
	}
	if got := labels(Rank("usr", cands, 10)); len(got) != 1 || got[0] != "users" {
		t.Errorf("fuzzy: %v", got)
	}
	if got := Rank("", cands, 10); len(got) != len(cands) {
		t.Errorf("empty prefix keeps all: %d", len(got))
	}
	if got := Rank("", cands, 2); len(got) != 2 {
		t.Errorf("limit: %d", len(got))
	}
	if got := Rank("zzz", cands, 10); len(got) != 0 {
		t.Errorf("no match: %v", got)
	}
}

func TestSelection(t *testing.T) {
	m := New([]Candidate{{Label: "a"}, {Label: "b"}, {Label: "c"}})
	if _, ok := m.Selected(); ok || m.Chosen().Label != "a" || m.Len() != 3 {
		t.Fatal("nothing selected at first; Chosen falls back to the first")
	}
	m.Next()
	m.Next()
	if c, _ := m.Selected(); c.Label != "b" {
		t.Fatalf("selected %q", c.Label)
	}
	m.Prev()
	m.Prev()
	if c, _ := m.Selected(); c.Label != "c" {
		t.Fatalf("Prev wraps: %q", c.Label)
	}
	m2 := New([]Candidate{{Label: "a"}, {Label: "b"}})
	m2.Prev()
	if c, _ := m2.Selected(); c.Label != "b" {
		t.Fatalf("Prev from nothing selects the last: %q", c.Label)
	}
	if (&Model{}).Chosen() != (Candidate{}) {
		t.Fatal("empty popup chooses nothing")
	}
}

func TestView(t *testing.T) {
	var items []Candidate
	for i := range 15 {
		items = append(items, Candidate{Label: fmt.Sprintf("col%02d", i), Kind: Column, Detail: "int"})
	}
	m := New(items)
	v := ansi.Strip(m.View(30, 6))
	ls := strings.Split(v, "\n")
	if len(ls) != 6 || !strings.Contains(v, "col   col00") || !strings.Contains(v, "int") {
		t.Fatalf("view:\n%s", v)
	}
	for _, l := range ls {
		if ansi.StringWidth(l) > 30 {
			t.Fatalf("wider than 30: %q", l)
		}
	}
	for range 11 {
		m.Next()
	}
	if v := ansi.Strip(m.View(30, 6)); !strings.Contains(v, "col10") {
		t.Fatalf("the selection scrolls into view:\n%s", v)
	}
}
