// Package rcmd is the terminal-free Redis helper package: key name
// escaping for display, the redis-cli quoted form, and SCAN patterns.
package rcmd

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Escape turns a key or value into one displayable line: printable valid
// UTF-8 is kept, \ becomes \\, newline and tab become \n and \t, and every
// other byte is written as \xNN (upper-case hex).
func Escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == utf8.RuneError && n == 1, !unicode.IsPrint(r):
			for _, c := range []byte(s[i : i+n]) {
				fmt.Fprintf(&b, `\x%02X`, c)
			}
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

// Quote returns the form redis-cli can take as one argument: s itself when
// it only has safe characters, otherwise s in double quotes with \" \\ \n
// \t and \xNN (lower-case hex) escapes. Printable valid UTF-8 stays as is.
func Quote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !safe(r) }) < 0 {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == utf8.RuneError && n == 1, !unicode.IsPrint(r):
			for _, c := range []byte(s[i : i+n]) {
				fmt.Fprintf(&b, `\x%02x`, c)
			}
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	b.WriteByte('"')
	return b.String()
}

func safe(r rune) bool {
	return r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		strings.ContainsRune("_:.-/{}@#+=,|", r))
}

// Pattern turns what the user typed into a SCAN MATCH pattern: input with
// a glob character (* ? [) is used as is, anything else matches as a
// substring. Blank input is "" (no filter).
func Pattern(input string) string {
	s := strings.TrimSpace(input)
	if s == "" || strings.ContainsAny(s, "*?[") {
		return s
	}
	return "*" + s + "*"
}

// Segments splits key on sep for grouping; empty segments are kept. An
// empty sep keeps the whole key as one segment.
func Segments(key, sep string) []string {
	if sep == "" {
		return []string{key}
	}
	return strings.Split(key, sep)
}
