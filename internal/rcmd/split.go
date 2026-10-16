package rcmd

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Line is one command in the console. Start/End are its byte offsets in
// the whole text (the newline is not included).
type Line struct {
	Text       string
	Start, End int
	Num        int // line number, from 1
}

// Lines splits text into command lines, skipping blank lines and comment
// lines (# or // after leading blanks).
func Lines(text string) []Line {
	var out []Line
	start := 0
	for num := 1; start <= len(text); num++ {
		end := strings.IndexByte(text[start:], '\n')
		next := start + end + 1
		if end < 0 {
			end, next = len(text)-start, len(text)+1
		}
		end += start
		if end > start && text[end-1] == '\r' {
			end--
		}
		s := strings.TrimSpace(text[start:end])
		if s != "" && !strings.HasPrefix(s, "#") && !strings.HasPrefix(s, "//") {
			out = append(out, Line{Text: text[start:end], Start: start, End: end, Num: num})
		}
		start = next
	}
	return out
}

// At is the line offset is on; ok is false on a blank or comment line.
func At(lines []Line, offset int) (Line, bool) {
	for _, l := range lines {
		if l.Start <= offset && offset <= l.End {
			return l, true
		}
	}
	return Line{}, false
}

// From is the line offset is on and every line after it (from the next
// command when offset is on a blank line).
func From(lines []Line, offset int) []Line {
	for i, l := range lines {
		if l.End >= offset {
			return lines[i:]
		}
	}
	return nil
}

// Within is the lines the selection from..to touches; a VISUAL selection
// counts whole lines, so a line starting exactly at to is included.
func Within(lines []Line, from, to int) []Line {
	var out []Line
	for _, l := range lines {
		if l.Start <= to && l.End >= from {
			out = append(out, l)
		}
	}
	return out
}

// SplitError is a Split failure; Col is where it is (characters, from 1).
type SplitError struct {
	Col int
	Msg string
}

func (e *SplitError) Error() string { return e.Msg }

// Token is one argument found by Tokens. Start/End are byte offsets in the
// line; Text is the unquoted content. Open means its quote is not closed.
type Token struct {
	Text         string
	Start, End   int
	Quoted, Open bool
}

// Split cuts one line into arguments the way redis-cli does.
func Split(line string) ([]string, error) {
	toks, err := scan(line, true)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range toks {
		out = append(out, t.Text)
	}
	return out, nil
}

// Tokens is the lenient Split for completion and highlighting: an
// unclosed quote makes the rest of the line the last token (Open).
func Tokens(line string) []Token {
	toks, _ := scan(line, false)
	return toks
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c >= 'a':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func col(line string, i int) int { return utf8.RuneCountInString(line[:i]) + 1 }

// scan is sdssplitargs: in "…" \n \r \t \b \a \xNN are escapes and any
// other \c is c (an invalid \x stays \x); in '…' only \' is one. A closing
// quote must be followed by a blank or the end of the line.
func scan(line string, strict bool) ([]Token, *SplitError) {
	var out []Token
	i := 0
	for {
		for i < len(line) && isSpace(line[i]) {
			i++
		}
		if i >= len(line) {
			return out, nil
		}
		tok := Token{Start: i}
		var b []byte
		var quote byte
		qstart := 0
	token:
		for {
			if i >= len(line) {
				if quote != 0 {
					if strict {
						c := col(line, qstart)
						return nil, &SplitError{Col: c, Msg: fmt.Sprintf("unterminated quote at col %d", c)}
					}
					tok.Open = true
				}
				break
			}
			c := line[i]
			switch {
			case quote == '"' && c == '\\' && i+3 < len(line) && line[i+1] == 'x' && isHex(line[i+2]) && isHex(line[i+3]):
				b = append(b, unhex(line[i+2])<<4|unhex(line[i+3]))
				i += 4
			case quote == '"' && c == '\\' && i+1 < len(line):
				switch e := line[i+1]; e {
				case 'n':
					b = append(b, '\n')
				case 'r':
					b = append(b, '\r')
				case 't':
					b = append(b, '\t')
				case 'b':
					b = append(b, '\b')
				case 'a':
					b = append(b, '\a')
				case 'x':
					b = append(b, '\\', 'x')
				default:
					b = append(b, e)
				}
				i += 2
			case quote == '\'' && c == '\\' && i+1 < len(line) && line[i+1] == '\'':
				b = append(b, '\'')
				i += 2
			case quote != 0 && c == quote:
				i++
				quote = 0
				if i < len(line) && !isSpace(line[i]) {
					if strict {
						return nil, &SplitError{Col: col(line, i), Msg: "closing quote must be followed by a space"}
					}
					break token
				}
			case quote != 0:
				b = append(b, c)
				i++
			case isSpace(c):
				break token
			case c == '"' || c == '\'':
				quote, qstart, tok.Quoted = c, i, true
				i++
			default:
				b = append(b, c)
				i++
			}
		}
		tok.Text, tok.End = string(b), i
		out = append(out, tok)
	}
}

// GlobEscape escapes * ? [ ] \ for a SCAN MATCH pattern, so a typed prefix
// matches literally.
func GlobEscape(s string) string {
	var b strings.Builder
	for i := range len(s) {
		if strings.IndexByte(`*?[]\`, s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Redact is the line shown in messages and kept in history (and the slow
// log's commands): the passwords of AUTH, HELLO … AUTH user pass, CONFIG SET
// requirepass / masterauth, ACL SETUSER's >pass and #hash rules and
// MIGRATE … AUTH / AUTH2 are ***.
func Redact(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = Quote(a)
	}
	is := func(i int, s string) bool { return i < len(args) && strings.EqualFold(args[i], s) }
	switch {
	case is(0, "AUTH") && len(args) > 1:
		parts[len(parts)-1] = "***"
	case is(0, "HELLO"):
		for i := 1; i+2 < len(args); i++ {
			if is(i, "AUTH") {
				parts[i+2] = "***"
				break
			}
		}
	case is(0, "CONFIG") && is(1, "SET"):
		for i := 2; i+1 < len(args); i += 2 {
			if is(i, "requirepass") || is(i, "masterauth") {
				parts[i+1] = "***"
			}
		}
	case is(0, "ACL") && is(1, "SETUSER"):
		for i := 3; i < len(args); i++ {
			if strings.HasPrefix(args[i], ">") || strings.HasPrefix(args[i], "#") {
				parts[i] = "***"
			}
		}
	case is(0, "MIGRATE"):
		for i := 1; i < len(args); i++ {
			switch {
			case is(i, "AUTH") && i+1 < len(args):
				parts[i+1] = "***"
			case is(i, "AUTH2") && i+2 < len(args):
				parts[i+2] = "***"
			}
		}
	}
	return strings.Join(parts, " ")
}
