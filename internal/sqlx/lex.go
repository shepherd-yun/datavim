// Package sqlx holds SQL text utilities: lexing, statement splitting and
// statement classification. It never talks to a database.
package sqlx

import (
	"strings"
	"unicode/utf8"
)

// Dialect selects the lexical rules of one engine.
type Dialect int

const (
	MySQL Dialect = iota
	Postgres
)

// DialectOf maps a driver name ("mysql", "postgres") to its dialect.
func DialectOf(driver string) Dialect {
	if driver == "postgres" {
		return Postgres
	}
	return MySQL
}

type tokKind int

const (
	tokWord    tokKind = iota // keyword or bare identifier
	tokIdent                  // quoted identifier; text is unquoted
	tokString                 // any string literal; text is the raw source
	tokPunct                  // one character
	tokComment                // -- # /* */
)

type token struct {
	kind     tokKind
	text     string
	pos, end int
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 ||
		c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// lex reads the token starting at src[i]; src[i] must not be whitespace.
func lex(src string, i int, d Dialect) token {
	c := src[i]
	switch {
	case c == '-' && strings.HasPrefix(src[i:], "--") && (d == Postgres || i+2 >= len(src) || isSpace(src[i+2])):
		return lineComment(src, i)
	case c == '#' && d == MySQL:
		return lineComment(src, i)
	case c == '/' && strings.HasPrefix(src[i:], "/*"):
		return blockComment(src, i, d == Postgres)
	case c == '\'':
		return quoted(src, i, '\'', tokString, d == MySQL)
	case (c == 'E' || c == 'e') && d == Postgres && i+1 < len(src) && src[i+1] == '\'':
		t := quoted(src, i+1, '\'', tokString, true)
		t.pos = i
		return t
	case c == '"' && d == MySQL:
		return quoted(src, i, '"', tokString, true)
	case c == '"':
		return quoted(src, i, '"', tokIdent, false)
	case c == '`' && d == MySQL:
		return quoted(src, i, '`', tokIdent, false)
	case c == '$' && d == Postgres:
		if t, ok := dollar(src, i); ok {
			return t
		}
		return token{kind: tokPunct, text: "$", pos: i, end: i + 1}
	case isWordByte(c):
		j := i
		for j < len(src) && isWordByte(src[j]) {
			j++
		}
		return token{kind: tokWord, text: src[i:j], pos: i, end: j}
	}
	_, size := utf8.DecodeRuneInString(src[i:])
	return token{kind: tokPunct, text: src[i : i+size], pos: i, end: i + size}
}

func lineComment(src string, i int) token {
	end := strings.IndexByte(src[i:], '\n')
	if end < 0 {
		end = len(src)
	} else {
		end += i
	}
	return token{kind: tokComment, text: src[i:end], pos: i, end: end}
}

func blockComment(src string, i int, nested bool) token {
	depth := 0
	j := i
	for j < len(src) {
		switch {
		case strings.HasPrefix(src[j:], "/*") && (nested || depth == 0):
			depth++
			j += 2
		case strings.HasPrefix(src[j:], "*/"):
			depth--
			j += 2
			if depth == 0 {
				return token{kind: tokComment, text: src[i:j], pos: i, end: j}
			}
		default:
			j++
		}
	}
	return token{kind: tokComment, text: src[i:], pos: i, end: len(src)}
}

// quoted reads a q-delimited token; a doubled q is an escaped q, and with
// backslash a backslash escapes the next byte.
func quoted(src string, i int, q byte, kind tokKind, backslash bool) token {
	var b strings.Builder
	j := i + 1
	for j < len(src) {
		c := src[j]
		switch {
		case backslash && c == '\\' && j+1 < len(src):
			b.WriteByte(c)
			b.WriteByte(src[j+1])
			j += 2
			continue
		case c == q && j+1 < len(src) && src[j+1] == q:
			b.WriteByte(q)
			j += 2
			continue
		case c == q:
			j++
			return finishQuoted(src, i, j, kind, b.String())
		}
		b.WriteByte(c)
		j++
	}
	return finishQuoted(src, i, len(src), kind, b.String())
}

func finishQuoted(src string, i, end int, kind tokKind, inner string) token {
	if kind == tokIdent {
		return token{kind: kind, text: inner, pos: i, end: end}
	}
	return token{kind: kind, text: src[i:end], pos: i, end: end}
}

// dollar reads a PostgreSQL $tag$...$tag$ string.
func dollar(src string, i int) (token, bool) {
	j := i + 1
	for j < len(src) && (isWordByte(src[j]) && src[j] != '$') {
		j++
	}
	if j >= len(src) || src[j] != '$' {
		return token{}, false
	}
	tag := src[i : j+1]
	if len(tag) > 2 && tag[1] >= '0' && tag[1] <= '9' {
		return token{}, false
	}
	body := j + 1
	if k := strings.Index(src[body:], tag); k >= 0 {
		end := body + k + len(tag)
		return token{kind: tokString, text: src[i:end], pos: i, end: end}, true
	}
	return token{kind: tokString, text: src[i:], pos: i, end: len(src)}, true
}

// codeTokens returns the tokens of src without comments.
func codeTokens(src string, d Dialect) []token {
	var out []token
	for i := 0; i < len(src); {
		if isSpace(src[i]) {
			i++
			continue
		}
		t := lex(src, i, d)
		if t.kind != tokComment {
			out = append(out, t)
		}
		i = t.end
	}
	return out
}
