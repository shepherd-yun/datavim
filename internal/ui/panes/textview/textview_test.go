package textview

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func lines(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprint("line ", i+1)
	}
	return out
}

func TestTextview(t *testing.T) {
	m := New()
	m.SetLines(lines(20))
	m.SetSize(30, 5)
	if v := ansi.Strip(m.View(true)); !strings.HasPrefix(v, "line 1 ") || strings.Count(v, "\n") != 4 {
		t.Fatalf("view:\n%s", v)
	}
	m.Bottom()
	v := strings.Split(ansi.Strip(m.View(true)), "\n")
	if strings.TrimSpace(v[0]) != "line 16" || strings.TrimSpace(v[4]) != "line 20" {
		t.Fatalf("bottom:\n%s", strings.Join(v, "\n"))
	}
	m.Top()
	if !m.Find("line 3", true) || m.Offset() != 2 {
		t.Fatalf("find: offset %d", m.Offset())
	}
	if m.Find("nope", true) {
		t.Fatal("found nope")
	}
	m.Move(100)
	if m.Offset() != 15 {
		t.Fatalf("move clamps: offset %d", m.Offset())
	}
	m.HalfPage(-1)
	if m.Offset() != 13 {
		t.Fatalf("half page: offset %d", m.Offset())
	}
	m.SetLines(lines(2))
	if m.Offset() != 0 {
		t.Fatalf("short text: offset %d", m.Offset())
	}
}

func TestTextviewWraps(t *testing.T) {
	m := New()
	m.SetSize(4, 3)
	m.SetLines([]string{"abcdefghij", "xy"})
	if v := ansi.Strip(m.View(true)); !strings.Contains(v, "abc…") {
		t.Fatalf("without wrap long lines are cut:\n%s", v)
	}
	m.SetWrap(true)
	v := strings.Split(ansi.Strip(m.View(true)), "\n")
	if strings.TrimSpace(v[0]) != "abcd" || strings.TrimSpace(v[2]) != "ij" {
		t.Fatalf("wrapped:\n%s", strings.Join(v, "\n"))
	}
	m.Bottom()
	if v := ansi.Strip(m.View(true)); !strings.Contains(v, "xy") {
		t.Fatalf("scrolls over wrapped lines:\n%s", v)
	}
}
