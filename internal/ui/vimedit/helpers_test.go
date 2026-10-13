package vimedit

import (
	"strings"
	"testing"
)

// parse splits s at "|" into the text and the cursor before that point.
func parse(t *testing.T, s string) (string, Pos) {
	t.Helper()
	i := strings.Index(s, "|")
	if i < 0 {
		t.Fatalf("no cursor in %q", s)
	}
	text := s[:i] + s[i+1:]
	b := newBuffer(text)
	return text, b.pos(i)
}

// show is the editor's text with "|" before the cursor.
func show(e *Editor) string {
	off := e.Offset()
	text := e.Text()
	return text[:off] + "|" + text[off:]
}

// keys turns "d2w<esc>" into key presses: <esc> <cr> <bs> <del> <tab>
// <space> <left> <c-r> name special keys, every other rune types itself.
func keys(s string) []Key {
	var out []Key
	for s != "" {
		if s[0] == '<' {
			if end := strings.IndexByte(s, '>'); end > 1 {
				name := s[1:end]
				s = s[end+1:]
				switch {
				case name == "cr":
					name = "enter"
				case name == "bs":
					name = "backspace"
				case name == "del":
					name = "delete"
				case strings.HasPrefix(name, "c-"):
					name = "ctrl+" + name[2:]
				}
				k := Key{Name: name}
				if name == "space" {
					k.Text = " "
				}
				out = append(out, k)
				continue
			}
		}
		r := []rune(s)[0]
		s = s[len(string(r)):]
		if r == ' ' {
			out = append(out, Key{Name: "space", Text: " "})
			continue
		}
		out = append(out, Key{Name: string(r), Text: string(r)})
	}
	return out
}

// feed sends keys to e and returns the last result.
func feed(e *Editor, s string) Result {
	var r Result
	for _, k := range keys(s) {
		r = e.Feed(k)
	}
	return r
}

// newAt is an editor holding s ("|" marks the cursor) in NORMAL mode.
func newAt(t *testing.T, s string) *Editor {
	t.Helper()
	text, p := parse(t, s)
	e := New()
	e.SetText(text)
	e.SetCursor(p)
	return e
}

// editCase is one table row: start text, keys, expected text and mode.
type editCase struct {
	start, keys, want string
	mode              Mode
}

func runCases(t *testing.T, cases []editCase) {
	t.Helper()
	for _, c := range cases {
		e := newAt(t, c.start)
		feed(e, c.keys)
		if got := show(e); got != c.want || e.Mode() != c.mode {
			t.Errorf("%q + %q = %q (%s), want %q (%s)", c.start, c.keys, got, e.Mode(), c.want, c.mode)
		}
	}
}
