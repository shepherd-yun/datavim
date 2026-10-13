// Package highlight splits SQL into colored spans using Chroma.
package highlight

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"github.com/shepherd-yun/sqlvim/internal/sqlx"
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
	sqlx.MySQL:    lexer("mysql"),
	sqlx.Postgres: lexer("postgresql"),
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
