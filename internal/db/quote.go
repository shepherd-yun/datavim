package db

import (
	"fmt"
	"strings"
)

// Quote wraps an identifier in q, doubling any embedded q.
func Quote(s string, q rune) string {
	qs := string(q)
	return qs + strings.ReplaceAll(s, qs, qs+qs) + qs
}

// LimitOffset appends a LIMIT/OFFSET clause; MySQL and PostgreSQL share this syntax.
func LimitOffset(sql string, limit, offset int) string {
	return fmt.Sprintf("%s LIMIT %d OFFSET %d", sql, limit, offset)
}
