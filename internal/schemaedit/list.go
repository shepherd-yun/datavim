package schemaedit

import (
	"strings"
	"unicode"
)

// splitList splits a typed column list at the commas outside parentheses
// and quotes, trimming the parts and dropping empty ones.
func splitList(s string) []string {
	var out []string
	depth, quote, start := 0, rune(0), 0
	add := func(end int) {
		if p := strings.TrimSpace(s[start:end]); p != "" {
			out = append(out, p)
		}
	}
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"' || r == '`':
			quote = r
		case r == '(':
			depth++
		case r == ')':
			depth = max(depth-1, 0)
		case r == ',' && depth == 0:
			add(i)
			start = i + 1
		}
	}
	add(len(s))
	return out
}

// unquote is the name an index column entry spells: "Odd Col" for
// `"Odd Col"`, the entry itself when it is not quoted.
func unquote(s string) string {
	if len(s) >= 2 {
		if q := s[0]; (q == '"' || q == '`') && s[len(s)-1] == q {
			return strings.ReplaceAll(s[1:len(s)-1], string(q)+string(q), string(q))
		}
	}
	return s
}

// isExpression reports whether an index entry is an expression, such as
// lower(email) or MySQL's "(expression)", rather than a column.
func isExpression(entry string) bool { return strings.ContainsRune(entry, '(') }

// nameWords is entries' column names, for making up an index name.
func nameWords(entries []string) []string {
	var out []string
	for _, e := range entries {
		if isExpression(e) {
			continue
		}
		w := strings.Map(func(r rune) rune {
			if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
			return '_'
		}, unquote(e))
		out = append(out, w)
	}
	return out
}
