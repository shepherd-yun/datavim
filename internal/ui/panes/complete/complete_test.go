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
		{Label: "name", Kind: Column},
		{Label: "nickname", Kind: Column},
		{Label: "users", Kind: Table},
		{Label: "NOT", Kind: Keyword},
		{Label: "NULL", Kind: Keyword},
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
	if c, ok := m.Selected(); !ok || c.Label != "a" || m.Chosen().Label != "a" || m.Len() != 3 {
		t.Fatal("the first candidate is selected at first")
	}
	m.Next()
	m.Next()
	if c, _ := m.Selected(); c.Label != "c" {
		t.Fatalf("selected %q", c.Label)
	}
	m.Next()
	if c, _ := m.Selected(); c.Label != "a" {
		t.Fatalf("Next wraps: %q", c.Label)
	}
	m.Prev()
	if c, _ := m.Selected(); c.Label != "c" {
		t.Fatalf("Prev wraps: %q", c.Label)
	}
	if (&Model{}).Chosen() != (Candidate{}) {
		t.Fatal("empty popup chooses nothing")
	}
}

func TestRankPutsExactMatchesFirst(t *testing.T) {
	cands := []Candidate{{Label: "order_id", Kind: Column}, {Label: "OR", Kind: Keyword}, {Label: "ORDER", Kind: Keyword}}
	if got := labels(Rank("or", cands, 10)); len(got) != 3 || got[0] != "OR" || got[1] != "order_id" {
		t.Fatalf("exact match first, then the rest in order: %v", got)
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
