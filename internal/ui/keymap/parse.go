package keymap

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ParseKeys reads a key sequence written the way Display shows it: "gg",
// "<leader>s", "<ctrl+d>", "f<". A "<" that does not start a "<name>" is
// the key "<" itself.
func ParseKeys(s string) ([]string, error) {
	if s == "" {
		return nil, errors.New("empty key sequence")
	}
	var out []string
	for i := 0; i < len(s); {
		if s[i] == '<' && i+1 < len(s) && s[i+1] >= 'a' && s[i+1] <= 'z' {
			end := strings.IndexByte(s[i+1:], '>')
			if end < 0 || !isKeyName(s[i+1:i+1+end]) {
				return nil, fmt.Errorf("unterminated or malformed <name> in %q", s)
			}
			name := s[i+1 : i+1+end]
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
