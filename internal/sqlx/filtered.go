package sqlx

import (
	"errors"
	"slices"
	"strconv"
	"strings"
)

// ErrNotSelect is returned for a statement that a filter cannot narrow.
var ErrNotSelect = errors.New("filter works on SELECT results")

// whereEnd ends a WHERE clause, or marks where a missing one goes.
var whereEnd = wordSet("GROUP", "HAVING", "WINDOW", "ORDER", "LIMIT", "OFFSET", "FETCH", "FOR", "LOCK", "UNION", "INTERSECT", "EXCEPT")

// Filtered narrows stmt to the rows matching cond, a condition without
// WHERE. With merge stmt is a single-table SELECT (ResultSource accepted
// it) and cond is ANDed into its WHERE. Otherwise stmt is wrapped:
// SELECT * FROM (stmt) q WHERE cond, a trailing ORDER BY that names only
// result columns (cols) or positions moving outside, since MySQL drops an
// ORDER BY without LIMIT inside a derived table; ordered is false when one
// could not be kept. Each condition closes on its own line so a trailing
// -- comment cannot swallow what follows.
func Filtered(d Dialect, stmt, cond string, merge bool, cols []string) (sql string, ordered bool, err error) {
	stmt = strings.TrimRight(strings.TrimSpace(stmt), "; \t\r\n")
	toks := codeTokens(stmt, d)
	if len(toks) == 0 || !isWord(toks[0], "SELECT") && !isWord(toks[0], "WITH") && !isPunct(toks[0], "(") {
		return "", false, ErrNotSelect
	}
	if cond == "" {
		return stmt, true, nil
	}
	if merge {
		return mergeWhere(toks, stmt, cond), true, nil
	}
	return wrapWhere(toks, stmt, cond, cols)
}

func mergeWhere(toks []token, stmt, cond string) string {
	from := depth0Index(toks, 0, "FROM")
	where := depth0Index(toks, from, "WHERE")
	if where < 0 {
		at := clauseAt(toks, from+1)
		if at < 0 {
			return stmt + "\nWHERE (" + cond + "\n)"
		}
		return stmt[:at] + "WHERE (" + cond + "\n) " + stmt[at:]
	}
	open := toks[where].end
	end := clauseAt(toks, where+1)
	rest := ""
	if end < 0 {
		end = len(stmt)
	} else {
		rest = " " + stmt[end:]
	}
	return stmt[:open] + " (" + strings.TrimSpace(stmt[open:end]) + "\n) AND (" + cond + "\n)" + rest
}

// clauseAt is the byte offset of the first top-level clause that ends a
// WHERE, from toks[from] on; -1 when there is none.
func clauseAt(toks []token, from int) int {
	depth := 0
	for j := from; j < len(toks); j++ {
		depth += depthDelta(toks[j])
		if depth == 0 && toks[j].kind == tokWord && whereEnd[upper(toks[j])] {
			return toks[j].pos
		}
	}
	return -1
}

func wrapWhere(toks []token, stmt, cond string, cols []string) (string, bool, error) {
	wrap := func(inner string) string { return "SELECT * FROM (\n" + inner + "\n) q WHERE (" + cond + "\n)" }
	order := lastOrderBy(toks)
	if order < 0 || topped(toks) {
		return wrap(stmt), true, nil // TOP keeps the ORDER BY inside meaningful
	}
	depth := 0
	for j := order + 2; j < len(toks); j++ {
		depth += depthDelta(toks[j])
		if depth == 0 && toks[j].kind == tokWord && (isWord(toks[j], "LIMIT") || isWord(toks[j], "OFFSET") || isWord(toks[j], "FETCH")) {
			return wrap(stmt), true, nil // LIMIT keeps the ORDER BY inside meaningful
		}
	}
	if !plainOrder(toks[order+2:], cols) {
		return wrap(stmt), false, nil
	}
	at := toks[order].pos
	return wrap(strings.TrimSpace(stmt[:at])) + "\n" + stmt[at:], true, nil
}

// topped reports a SELECT TOP (T-SQL) at the top level.
func topped(toks []token) bool {
	depth := 0
	for j := range toks {
		depth += depthDelta(toks[j])
		if depth == 0 && isWord(toks[j], "SELECT") && j+1 < len(toks) &&
			(isWord(toks[j+1], "TOP") || isWord(toks[j+1], "DISTINCT") && j+2 < len(toks) && isWord(toks[j+2], "TOP")) {
			return true
		}
	}
	return false
}

// lastOrderBy is the index of the top-level ORDER of a trailing ORDER BY.
func lastOrderBy(toks []token) int {
	depth, at := 0, -1
	for j := range toks {
		depth += depthDelta(toks[j])
		if depth == 0 && isWord(toks[j], "ORDER") && j+1 < len(toks) && isWord(toks[j+1], "BY") {
			at = j
		}
	}
	return at
}

// plainOrder reports ORDER BY terms that are each a result column or a
// position, optionally with ASC/DESC and NULLS FIRST/LAST.
func plainOrder(toks []token, cols []string) bool {
	var term []token
	ok := func() bool {
		if len(term) == 0 {
			return false
		}
		name := term[0]
		named := isName(name) && slices.ContainsFunc(cols, func(c string) bool { return strings.EqualFold(c, name.text) })
		if _, err := strconv.Atoi(name.text); err != nil && !named {
			return false
		}
		for _, t := range term[1:] {
			if !isWord(t, "ASC") && !isWord(t, "DESC") && !isWord(t, "NULLS") && !isWord(t, "FIRST") && !isWord(t, "LAST") {
				return false
			}
		}
		return true
	}
	for _, t := range toks {
		if isPunct(t, ",") {
			if !ok() {
				return false
			}
			term = nil
			continue
		}
		term = append(term, t)
	}
	return ok()
}

// HasOrderBy reports a top-level ORDER BY in stmt.
func HasOrderBy(stmt string, d Dialect) bool { return lastOrderBy(codeTokens(stmt, d)) >= 0 }
