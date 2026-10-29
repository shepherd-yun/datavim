package textutil

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSanitize(t *testing.T) {
	got := Sanitize("a\nb\tc\r\x1b[31md\x7f")
	if want := "a↵b c�[31md�"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFit(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{"abc", 5, "abc  "},
		{"abcdef", 4, "abc…"},
		{"李雷abc", 5, "李雷…"},
		{"李雷", 4, "李雷"},
		{"abc", 0, ""},
	}
	for _, tt := range tests {
		if got := Fit(tt.in, tt.width); got != tt.want {
			t.Errorf("Fit(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
	}
	styled := lipgloss.NewStyle().Bold(true).Render("hello world")
	if w := ansi.StringWidth(Fit(styled, 6)); w != 6 {
		t.Errorf("styled width = %d, want 6", w)
	}
}

func TestPadLines(t *testing.T) {
	if got := PadLines("a\nb", 3); got != "a\nb\n" {
		t.Errorf("pad = %q", got)
	}
	if got := PadLines("a\nb\nc", 2); got != "a\nb" {
		t.Errorf("trim = %q", got)
	}
	if got := PadLines("", 2); got != "\n" {
		t.Errorf("empty = %q", got)
	}
}

func TestWrap(t *testing.T) {
	got := Wrap("李雷abcd", 4)
	if strings.Join(got, "|") != "李雷|abcd" {
		t.Fatalf("got %q", got)
	}
	if len(Wrap("", 4)) != 1 {
		t.Fatal("empty input wraps to one empty line")
	}
}

func TestTable(t *testing.T) {
	got := Table([]string{"Name", "Type"}, [][]string{{"id", "bigint"}, {"名字", "text"}})
	want := []string{
		"Name  Type",
		"────  ──────",
		"id    bigint",
		"名字  text",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
	if empty := Table([]string{"A"}, nil); empty[len(empty)-1] != "(none)" {
		t.Fatalf("empty table = %q", empty)
	}
}

func TestClamp(t *testing.T) {
	if Clamp(5, 0, 3) != 3 || Clamp(-1, 0, 3) != 0 || Clamp(2, 0, -1) != 0 {
		t.Fatal("clamp")
	}
}

func TestCycle(t *testing.T) {
	for _, tt := range []struct{ v, dir, n, want int }{
		{0, 1, 3, 1}, {2, 1, 3, 0}, {0, -1, 3, 2}, {1, -1, 3, 0}, {0, 1, 0, 0},
	} {
		if got := Cycle(tt.v, tt.dir, tt.n); got != tt.want {
			t.Errorf("Cycle(%d, %d, %d) = %d, want %d", tt.v, tt.dir, tt.n, got, tt.want)
		}
	}
}

func TestSanitizeText(t *testing.T) {
	if got := SanitizeText("a\tb\r\n\x1b[31mc\n"); got != "a b\n�[31mc\n" {
		t.Fatalf("SanitizeText = %q", got)
	}
}

func TestFastJSON(t *testing.T) {
	for _, tt := range []struct {
		in, want string
		ok       bool
	}{
		{`[{"@type":"a.B","dictCode":28L,"sort":1L,"f":1.5F,"s":"28L"}]`, `[{"@type":"a.B","dictCode":28,"sort":1,"f":1.5,"s":"28L"}]`, true},
		{`{"ids":Set[1L,2L],"t":HashSet["a"]}`, `{"ids":[1,2],"t":["a"]}`, true},
		{`{"a":"say \"Set[\" 1L"}`, `{"a":"say \"Set[\" 1L"}`, true},
		{`{"a":1}`, `{"a":1}`, true},
		{`hello 1L`, `hello 1`, false},
		{`{"a":ABCL}`, `{"a":ABCL}`, false},
	} {
		got, ok := FastJSON(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("FastJSON(%s) = %s %v, want %s %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}
