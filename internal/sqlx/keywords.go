package sqlx

import (
	"sort"
	"strings"
)

var (
	commonKeywords = []string{
		"ADD", "ALL", "ALTER", "AND", "ANY", "AS", "ASC", "AVG", "BEGIN", "BETWEEN", "BY",
		"CASE", "CAST", "CHECK", "COALESCE", "COLUMN", "COMMIT", "CONSTRAINT", "COUNT", "CREATE",
		"CROSS", "DATABASE", "DEFAULT", "DELETE", "DESC", "DISTINCT", "DROP", "ELSE", "END",
		"EXCEPT", "EXISTS", "EXPLAIN", "FALSE", "FOREIGN", "FROM", "FULL", "FUNCTION", "GRANT",
		"GROUP", "HAVING", "IN", "INDEX", "INNER", "INSERT", "INTERSECT", "INTO", "IS", "JOIN",
		"KEY", "LEFT", "LIKE", "LIMIT", "MAX", "MIN", "NATURAL", "NOT", "NULL", "NULLIF",
		"OFFSET", "ON", "OR", "ORDER", "OUTER", "OVER", "PARTITION", "PRIMARY", "PROCEDURE",
		"REFERENCES", "RENAME", "REVOKE", "RIGHT", "ROLLBACK", "SAVEPOINT", "SCHEMA", "SELECT",
		"SET", "SUM", "TABLE", "THEN", "TO", "TRANSACTION", "TRIGGER", "TRUE", "TRUNCATE",
		"UNION", "UNIQUE", "UPDATE", "USING", "VALUES", "VIEW", "WHEN", "WHERE", "WINDOW", "WITH",
	}
	mysqlKeywords = []string{
		"AUTO_INCREMENT", "CHARSET", "COLLATE", "COLUMNS", "DATABASES", "DELIMITER", "DESCRIBE",
		"DUPLICATE", "ENGINE", "IFNULL", "IGNORE", "INTERVAL", "REGEXP", "REPLACE", "SHOW",
		"STRAIGHT_JOIN", "TABLES", "UNSIGNED", "USE",
	}
	postgresKeywords = []string{
		"ARRAY", "CONFLICT", "COPY", "DO", "FETCH", "FILTER", "FIRST", "ILIKE", "LAST", "LATERAL",
		"MATERIALIZED", "NEXT", "NOTHING", "NULLS", "ONLY", "RETURNING", "ROW", "ROWS", "SIMILAR",
		"VACUUM",
	}
	keywordLists = map[Dialect][]string{
		MySQL:    sortedUnique(commonKeywords, mysqlKeywords),
		Postgres: sortedUnique(commonKeywords, postgresKeywords),
	}
	keywordSets = map[Dialect]map[string]bool{
		MySQL:    wordSet(keywordLists[MySQL]...),
		Postgres: wordSet(keywordLists[Postgres]...),
	}
	// reserved words must be quoted when used as identifiers.
	reserved = wordSet(
		"ALL", "AND", "ANY", "AS", "ASC", "BETWEEN", "BY", "CASE", "CHECK", "COLUMN", "CONSTRAINT",
		"CREATE", "CROSS", "DEFAULT", "DELETE", "DESC", "DISTINCT", "DROP", "ELSE", "END", "EXISTS",
		"FALSE", "FOR", "FOREIGN", "FROM", "FULL", "GRANT", "GROUP", "HAVING", "IN", "INDEX",
		"INNER", "INSERT", "INTO", "IS", "JOIN", "KEY", "LEFT", "LIKE", "LIMIT", "NATURAL", "NOT",
		"NULL", "ON", "OR", "ORDER", "OUTER", "PRIMARY", "REFERENCES", "RIGHT", "SELECT", "SET",
		"TABLE", "THEN", "TO", "TRUE", "UNION", "UNIQUE", "UPDATE", "USER", "USING", "VALUES",
		"WHEN", "WHERE", "WITH",
	)
)

func sortedUnique(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, w := range l {
			if !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Keywords lists the keywords offered for completion: upper case, sorted.
// The slice is shared; do not modify it.
func Keywords(d Dialect) []string { return keywordLists[d] }

func isKeyword(w string, d Dialect) bool { return keywordSets[d][strings.ToUpper(w)] }

// QuoteIfNeeded returns name as it must be written in SQL: bare when it is
// a plain identifier, otherwise quoted (` for MySQL, " for PostgreSQL).
func QuoteIfNeeded(name string, d Dialect) string {
	if plainIdent(name, d) && !reserved[strings.ToUpper(name)] {
		return name
	}
	q := "`"
	if d == Postgres {
		q = `"`
	}
	return q + strings.ReplaceAll(name, q, q+q) + q
}

func plainIdent(s string, d Dialect) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
			if d == Postgres { // unquoted names fold to lower case
				return false
			}
		case r >= '0' && r <= '9' || r == '$':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
