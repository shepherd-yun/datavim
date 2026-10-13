// Package highlight splits SQL into colored spans using Chroma.
package highlight

import (
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

func classOf(t chroma.TokenType) Class {
	switch {
	case t == chroma.OperatorWord: // AND, OR, NOT
		return Keyword
	case t == chroma.KeywordType, t == chroma.NameBuiltin, t == chroma.NameFunction:
		return Builtin
	case t == chroma.LiteralStringName: // PostgreSQL "quoted identifiers"
		return Plain
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
