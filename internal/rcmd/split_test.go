package rcmd

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestSplit(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"SET a b", []string{"SET", "a", "b"}},
		{`set  "a b"  c`, []string{"set", "a b", "c"}},
		{`"\x41\n"`, []string{"A\n"}},
		{`"\r\t\b\a\"\\"`, []string{"\r\t\b\a\"\\"}},
		{`'it\'s'`, []string{"it's"}},
		{`'a\nb'`, []string{`a\nb`}},
		{`""`, []string{""}},
		{`"中\x"`, []string{`中\x`}},
		{`"\xzz"`, []string{`\xzz`}},
		{"  GET   a  ", []string{"GET", "a"}},
		{"", nil},
	} {
		got, err := Split(tc.in)
		if err != nil {
			t.Errorf("Split(%q) error %v", tc.in, err)
			continue
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("Split(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSplitErrors(t *testing.T) {
	for _, tc := range []struct {
		in  string
		col int
		msg string
	}{
		{`"a"b`, 4, "closing quote must be followed by a space"},
		{`'a'b`, 4, "closing quote must be followed by a space"},
		{`"abc`, 1, "unterminated quote at col 1"},
		{`SET 中 "abc`, 7, "unterminated quote at col 7"},
		{`'abc`, 1, "unterminated quote at col 1"},
	} {
		_, err := Split(tc.in)
		var se *SplitError
		if !errors.As(err, &se) {
			t.Errorf("Split(%q) error = %v, want SplitError", tc.in, err)
			continue
		}
		if se.Col != tc.col || !strings.Contains(se.Msg, tc.msg) || err.Error() != se.Msg {
			t.Errorf("Split(%q) = col %d %q, want col %d %q", tc.in, se.Col, se.Msg, tc.col, tc.msg)
		}
	}
}

func TestLines(t *testing.T) {
	text := "GET a\n\n# c\n  // d\nSET b 1"
	lines := Lines(text)
	if len(lines) != 2 {
		t.Fatalf("Lines = %+v", lines)
	}
	for i, want := range []struct {
		text string
		num  int
	}{{"GET a", 1}, {"SET b 1", 5}} {
		l := lines[i]
		if l.Text != want.text || l.Num != want.num || text[l.Start:l.End] != want.text {
			t.Errorf("line %d = %+v, want %q num %d", i, l, want.text, want.num)
		}
	}
	third := strings.Index(text, "# c") + 1
	if _, ok := At(lines, third); ok {
		t.Errorf("At(comment line) ok")
	}
	if l, ok := At(lines, 2); !ok || l.Num != 1 {
		t.Errorf("At(2) = %+v %v", l, ok)
	}
	if l, ok := At(lines, len(text)); !ok || l.Num != 5 {
		t.Errorf("At(end) = %+v %v", l, ok)
	}
	if got := From(lines, third); len(got) != 1 || got[0].Num != 5 {
		t.Errorf("From(comment) = %+v", got)
	}
	if got := From(lines, 0); len(got) != 2 {
		t.Errorf("From(0) = %+v", got)
	}
	fifth := strings.Index(text, "SET")
	if got := Within(lines, 2, fifth); len(got) != 2 {
		t.Errorf("Within = %+v", got)
	}
	if got := Within(lines, 2, 4); len(got) != 1 || got[0].Num != 1 {
		t.Errorf("Within(line 1) = %+v", got)
	}
	if got := Lines("\r\n  \n"); len(got) != 0 {
		t.Errorf("blank Lines = %+v", got)
	}
	if got := Lines("GET a\r\nGET b"); len(got) != 2 || got[0].Text != "GET a" {
		t.Errorf("CRLF Lines = %+v", got)
	}
}

func TestTokens(t *testing.T) {
	toks := Tokens(`HSET "a b`)
	if len(toks) != 2 {
		t.Fatalf("Tokens = %+v", toks)
	}
	if toks[0] != (Token{Text: "HSET", Start: 0, End: 4}) {
		t.Errorf("token 0 = %+v", toks[0])
	}
	if got := toks[1]; !got.Open || !got.Quoted || got.Text != "a b" || got.Start != 5 || got.End != 9 {
		t.Errorf("token 1 = %+v", got)
	}
	toks = Tokens(`GET "x y" 10`)
	if len(toks) != 3 || toks[1].Text != "x y" || toks[1].Open || !toks[1].Quoted || toks[1].End != 9 || toks[2].Start != 10 {
		t.Errorf("Tokens closed = %+v", toks)
	}
	if got := Tokens("   "); len(got) != 0 {
		t.Errorf("blank Tokens = %+v", got)
	}
}

func TestGlobEscape(t *testing.T) {
	if got := GlobEscape(`a*b?[c]\`); got != `a\*b\?\[c\]\\` {
		t.Errorf("GlobEscape = %q", got)
	}
	if got := GlobEscape("user:1"); got != "user:1" {
		t.Errorf("GlobEscape plain = %q", got)
	}
}

func TestRedact(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"AUTH", "secret"}, "AUTH ***"},
		{[]string{"auth", "u", "secret"}, "auth u ***"},
		{[]string{"HELLO", "3", "AUTH", "u", "p"}, "HELLO 3 AUTH u ***"},
		{[]string{"hello", "3", "auth", "u", "p", "SETNAME", "x"}, "hello 3 auth u *** SETNAME x"},
		{[]string{"SET", "a b", "1"}, `SET "a b" 1`},
		{[]string{"AUTH"}, "AUTH"},
		{[]string{"CONFIG", "SET", "requirepass", "s3cret"}, "CONFIG SET requirepass ***"},
		{[]string{"config", "set", "maxmemory", "1gb", "masterauth", "s3cret"}, "config set maxmemory 1gb masterauth ***"},
		{[]string{"CONFIG", "GET", "requirepass"}, "CONFIG GET requirepass"},
		{[]string{"ACL", "SETUSER", "u", "on", ">s3cret", "#abc", "~*", "+@read"}, `ACL SETUSER u on *** *** "~*" +@read`},
		{[]string{"MIGRATE", "h", "6379", "k", "0", "5000", "AUTH", "s3cret"}, "MIGRATE h 6379 k 0 5000 AUTH ***"},
		{[]string{"MIGRATE", "h", "6379", "k", "0", "5000", "AUTH2", "u", "s3cret", "KEYS", "a"}, "MIGRATE h 6379 k 0 5000 AUTH2 u *** KEYS a"},
		{nil, ""},
	} {
		if got := Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
