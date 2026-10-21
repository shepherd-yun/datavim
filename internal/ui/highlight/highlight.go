// Package highlight splits SQL (and JSON / Protobuf schemas) into colored
// spans using Chroma.
package highlight

import (
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"github.com/shepherd-yun/datavim/internal/rcmd"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// Class is the color class of a span.
type Class int

const (
	Plain Class = iota
	Keyword
	Builtin // types and built-in functions
	String
	Number
	Comment
	Operator
)

// Span is a run of source text with one class; offsets are bytes.
type Span struct {
	Start, End int
	Class      Class
}

// Lexers are used from the UI goroutine only.
var sqlLexers = map[sqlx.Dialect]chroma.Lexer{
	sqlx.MySQL:     lexer("mysql"),
	sqlx.Postgres:  lexer("postgresql"),
	sqlx.SQLServer: lexer("tsql"),
	sqlx.Oracle:    lexer("sql"), // Chroma has no PL/SQL lexer
}

func lexer(name string) chroma.Lexer {
	if l := lexers.Get(name); l != nil {
		return chroma.Coalesce(l)
	}
	return nil
}

// SQL classifies src. The spans cover src exactly, in order. If Chroma
// cannot reproduce the text token by token, all of it is Plain.
func SQL(src string, d sqlx.Dialect) []Span {
	if src == "" {
		return nil
	}
	plain := []Span{{Start: 0, End: len(src), Class: Plain}}
	l := sqlLexers[d]
	if l == nil {
		return plain
	}
	it, err := l.Tokenise(&chroma.TokeniseOptions{State: "root"}, src)
	if err != nil {
		return plain
	}
	var out []Span
	off := 0
	quoted := false // inside a MySQL `quoted identifier`
	for t := it(); t != chroma.EOF; t = it() {
		end := off + len(t.Value)
		if end > len(src) || src[off:end] != t.Value {
			return plain
		}
		if end == off {
			continue
		}
		c := classOf(t.Type)
		// Chroma lexes `name` as operator, name (or keyword), operator; the
		// whole quoted identifier is a plain name. Each backtick toggles,
		// so an escaped `` inside one keeps it open.
		if t.Type == chroma.Operator && strings.Trim(t.Value, "`") == "" {
			quoted = quoted != (len(t.Value)%2 == 1)
			c = Plain
		} else if quoted {
			c = Plain
		}
		if d == sqlx.SQLServer && t.Type == chroma.Operator && (t.Value == "[" || t.Value == "]") {
			c = Plain // a [quoted identifier]
		}
		if n := len(out); c == String && n > 0 && off > 0 && (src[off-1] == 'N' || src[off-1] == 'n') &&
			d == sqlx.SQLServer && (off == 1 || !isWordByte(src[off-2])) {
			// N'…' is one Unicode string literal
			out[n-1].End--
			if out[n-1].End == out[n-1].Start {
				out = out[:n-1]
			}
			off--
		}
		if n := len(out); n > 0 && out[n-1].Class == c {
			out[n-1].End = end
		} else {
			out = append(out, Span{Start: off, End: end, Class: c})
		}
		off = end
	}
	if off < len(src) {
		out = append(out, Span{Start: off, End: len(src), Class: Plain})
	}
	return out
}

func isWordByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func classOf(t chroma.TokenType) Class {
	switch {
	case t == chroma.OperatorWord: // AND, OR, NOT
		return Keyword
	case t == chroma.KeywordType, t == chroma.NameBuiltin, t == chroma.NameFunction:
		return Builtin
	case t == chroma.LiteralStringName: // PostgreSQL "quoted identifiers"
		return Plain
	case t == chroma.NameConstant: // NULL, TRUE, FALSE in MySQL
		return Keyword
	case t.InCategory(chroma.Keyword):
		return Keyword
	case t.InCategory(chroma.Comment):
		return Comment
	case t.InSubCategory(chroma.LiteralString):
		return String
	case t.InSubCategory(chroma.LiteralNumber):
		return Number
	case t.InCategory(chroma.Operator):
		return Operator
	}
	return Plain
}

// Highlighter highlights a growing, changing text such as the console
// editor's. It splits the text into statements and the gaps between them
// and keeps the spans of each piece, so an edit re-highlights only the
// piece it touches. Use it from one goroutine.
type Highlighter struct {
	d      sqlx.Dialect
	cache  map[string][]Span // piece text -> spans relative to the piece
	misses int               // pieces highlighted (for tests)
}

// NewHighlighter returns an empty highlighter for d.
func NewHighlighter(d sqlx.Dialect) *Highlighter {
	return &Highlighter{d: d, cache: map[string][]Span{}}
}

// Spans classifies src like SQL does.
func (h *Highlighter) Spans(src string) []Span {
	if src == "" {
		return nil
	}
	var bounds []int
	for _, st := range sqlx.Split(src, h.d) {
		bounds = append(bounds, st.Start, st.End)
	}
	bounds = append(bounds, len(src))
	next := make(map[string][]Span, len(bounds))
	var out []Span
	at := 0
	for _, b := range bounds {
		if b <= at {
			continue
		}
		piece := src[at:b]
		spans, ok := h.cache[piece]
		if !ok {
			spans = SQL(piece, h.d)
			h.misses++
		}
		next[piece] = spans
		for _, s := range spans {
			s.Start, s.End = s.Start+at, s.End+at
			if n := len(out); n > 0 && out[n-1].Class == s.Class && out[n-1].End == s.Start {
				out[n-1].End = s.End
			} else {
				out = append(out, s)
			}
		}
		at = b
	}
	h.cache = next
	return out
}

// Redis classifies a redis console: the command (and a container's
// subcommand) is a Keyword, quoted arguments are Strings, numbers are
// Numbers and # or // lines are Comments.
func Redis(src string) []Span {
	var out []Span
	start := 0
	for line := range strings.SplitSeq(src, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
		case strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//"):
			out = append(out, Span{Start: start, End: start + len(line), Class: Comment})
		default:
			toks := rcmd.Tokens(line)
			words := 1
			if c, _ := rcmd.Lookup([]string{toks[0].Text}); c.Subcommands {
				words = 2
			}
			for i, tok := range toks {
				s := Span{Start: start + tok.Start, End: start + tok.End}
				switch {
				case i < words && !tok.Quoted:
					s.Class = Keyword
				case tok.Quoted:
					s.Class = String
				case isNumber(tok.Text):
					s.Class = Number
				default:
					continue
				}
				out = append(out, s)
			}
		}
		start += len(line) + 1
	}
	return out
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil && s != "" && strings.IndexFunc(s, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }) < 0
}

// codeLexers are the lexers of Code, by language name.
var codeLexers = map[string]chroma.Lexer{
	"json":     lexer("json"),
	"protobuf": lexer("protobuf"),
}

// Code classifies src in lang ("json" or "protobuf"). Like SQL, the spans
// cover src exactly; an unknown language or text Chroma cannot reproduce
// is all Plain.
func Code(src, lang string) []Span {
	if src == "" {
		return nil
	}
	plain := []Span{{Start: 0, End: len(src), Class: Plain}}
	l := codeLexers[lang]
	if l == nil {
		return plain
	}
	it, err := l.Tokenise(&chroma.TokeniseOptions{State: "root"}, src)
	if err != nil {
		return plain
	}
	var out []Span
	off := 0
	for t := it(); t != chroma.EOF; t = it() {
		end := off + len(t.Value)
		if end > len(src) || src[off:end] != t.Value {
			return plain
		}
		if end == off {
			continue
		}
		c := classOf(t.Type)
		if n := len(out); n > 0 && out[n-1].Class == c {
			out[n-1].End = end
		} else {
			out = append(out, Span{Start: off, End: end, Class: c})
		}
		off = end
	}
	if off < len(src) {
		out = append(out, Span{Start: off, End: len(src), Class: Plain})
	}
	return out
}
