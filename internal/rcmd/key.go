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

// Match reports whether s matches the SCAN MATCH glob pattern as Redis
// matches it: * ? [abc] [^a] [a-z] and \ escapes, byte by byte.
func Match(pattern, s string) bool {
	if pattern == "" {
		return s == ""
	}
	switch pattern[0] {
	case '*':
		for i := 0; i <= len(s); i++ {
			if Match(pattern[1:], s[i:]) {
				return true
			}
		}
		return false
	case '?':
		return s != "" && Match(pattern[1:], s[1:])
	case '[':
		if s == "" {
			return false
		}
		end := strings.IndexByte(pattern[1:], ']')
		if end < 0 {
			return s[0] == '[' && Match(pattern[1:], s[1:])
		}
		class := pattern[1 : 1+end]
		not := strings.HasPrefix(class, "^")
		if not {
			class = class[1:]
		}
		in := false
		for i := 0; i < len(class); i++ {
			switch {
			case class[i] == '\\' && i+1 < len(class):
				i++
				in = in || class[i] == s[0]
			case i+2 < len(class) && class[i+1] == '-':
				lo, hi := class[i], class[i+2]
				if lo > hi {
					lo, hi = hi, lo
				}
				in = in || s[0] >= lo && s[0] <= hi
				i += 2
			default:
				in = in || class[i] == s[0]
			}
		}
		return in != not && Match(pattern[2+end:], s[1:])
	case '\\':
		if len(pattern) > 1 {
			return s != "" && s[0] == pattern[1] && Match(pattern[2:], s[1:])
		}
	}
	return s != "" && s[0] == pattern[0] && Match(pattern[1:], s[1:])
}
