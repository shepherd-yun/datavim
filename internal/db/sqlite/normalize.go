package sqlite

import "strings"

// normalizeType spells a declared type as TableSchema reports it: lower case,
// without folding affinities (VARCHAR(50) stays varchar(50)); a column with no
// type is "any".
func normalizeType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "" {
		return "any"
	}
	return t
}
