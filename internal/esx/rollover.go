package esx

import (
	"regexp"
	"strings"
)

// rolloverSuffix is a date (2026.10.21, 2026-10, 20261021) or a rollover
// sequence number of at least six digits at the end of a name.
var rolloverSuffix = regexp.MustCompile(`^(.*?[^-_.][-_.])(?:\d{4}[-._]?\d{2}(?:[-._]?\d{2})?|\d{6,})$`)

// RolloverPrefix is the part of a dated or rolled-over index name before
// its date or sequence ("logs-app-2026.10.21" → "logs-app-").
func RolloverPrefix(index string) (string, bool) {
	prefix, ok := "", false
	name := index
	for {
		m := rolloverSuffix.FindStringSubmatch(name)
		if m == nil {
			return prefix, ok
		}
		prefix, ok = m[1], true
		name = strings.TrimRight(m[1], "-_.")
	}
}
