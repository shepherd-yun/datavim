package keymap

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ParseKeys reads a key sequence written the way Display shows it: "gg",
// "<leader>s", "<ctrl+d>", "f<". A "<" not followed by a name of two or
// more characters is the key "<" itself ("f<a" is f, <, a).
func ParseKeys(s string) ([]string, error) {
	if s == "" {
		return nil, errors.New("empty key sequence")
	}
	var out []string
	for i := 0; i < len(s); {
		if word := nameWord(s[i+1:]); s[i] == '<' && len(word) >= 2 {
			end := len(word)
			if i+1+end >= len(s) || s[i+1+end] != '>' || !isKeyName(word) {
				return nil, fmt.Errorf("unterminated or malformed <name> in %q", s)
			}
			name := word
			if name == "leader" {
				name = LeaderToken
			}
			out = append(out, name)
			i += end + 2
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		out = append(out, string(r))
		i += size
	}
	return out, nil
}

// nameWord is the run of key-name characters s starts with, or "" unless
// it starts with a letter.
func nameWord(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return ""
	}
	n := 0
	for n < len(s) && (s[n] >= 'a' && s[n] <= 'z' || s[n] >= '0' && s[n] <= '9' || s[n] == '+' || s[n] == '-') {
		n++
	}
	return s[:n]
}

// isKeyName accepts Bubble Tea key names: "ctrl+d", "shift+tab", "f12".
func isKeyName(n string) bool {
	if len(n) < 2 || strings.HasSuffix(n, "+") {
		return false
	}
	for _, r := range n {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '+' && r != '-' {
			return false
		}
	}
	return true
}

// parseLeader accepts one key: ",", "space" or a named key like "<tab>";
// "" is space.
func parseLeader(s string) (string, error) {
	if s == "" {
		return "space", nil
	}
	if utf8.RuneCountInString(s) == 1 {
		return s, nil
	}
	if s == "space" {
		return s, nil
	}
	if name, ok := strings.CutPrefix(s, "<"); ok {
		if name, ok = strings.CutSuffix(name, ">"); ok && isKeyName(name) && name != "leader" {
			return name, nil
		}
	}
	return "", fmt.Errorf("leader %q must be one key, like \",\" or \"space\"", s)
}
