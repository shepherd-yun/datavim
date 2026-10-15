package sqlx

import (
	"fmt"
	"strings"
)

// Source is the table a SELECT reads its rows from, when each result
// column is one of its columns.
type Source struct {
	Qualifier string // database (MySQL) or schema (PostgreSQL) written before the name
	Table     string
	Columns   []string // the table column behind each result column, as written
}

var afterTable = map[string]string{
	"JOIN": "JOIN", "STRAIGHT_JOIN": "JOIN", "UNION": "UNION", "INTERSECT": "INTERSECT",
	"EXCEPT": "EXCEPT", "GROUP": "GROUP BY", "HAVING": "HAVING", "WINDOW": "a window function",
}

// selectItem is one entry of a select list: * or t.*, or [t.]col [[AS] alias].
type selectItem struct {
	star      bool
	qualifier string
	column    string
	output    string // the name the result gives it
}

// ResultSource reports the table behind the rows stmt returns, cols being
// the result's column names, or why the rows cannot be traced to one table
// ("" when they can): only a single-table SELECT of plain columns can.
func ResultSource(d Dialect, stmt string, cols []string) (Source, string) {
	toks := codeTokens(stmt, d)
	switch {
	case len(toks) > 0 && isWord(toks[0], "WITH"):
		return Source{}, "WITH"
	case len(toks) == 0 || !isWord(toks[0], "SELECT"):
		return Source{}, "not a SELECT"
	}
	start := 1
	if start < len(toks) && (isWord(toks[start], "DISTINCT") || isWord(toks[start], "DISTINCTROW")) {
		return Source{}, "DISTINCT"
	}
	if start < len(toks) && isWord(toks[start], "ALL") {
		start++
	}
	from := depth0Index(toks, start, "FROM")
	if from < 0 {
		return Source{}, "no FROM"
	}
	if from+1 < len(toks) && isPunct(toks[from+1], "(") {
		return Source{}, "a subquery in FROM"
	}
	use, next, ok := tableUse(toks, from+1)
	if !ok {
		return Source{}, "not a single-table SELECT"
	}
	depth := 0
	for j := next; j < len(toks); j++ {
		depth += depthDelta(toks[j])
		if depth != 0 {
			continue
		}
		if isPunct(toks[j], ",") {
			return Source{}, "JOIN"
		}
		if toks[j].kind == tokWord {
			if why, bad := afterTable[upper(toks[j])]; bad {
				return Source{}, why
			}
		}
	}
	items, why := selectItems(toks[start:from])
	if why != "" {
		return Source{}, why
	}
	src := Source{Qualifier: use.Qualifier, Table: use.Name}
	stars := false
	for i, it := range items {
		if it.qualifier != "" && !strings.EqualFold(it.qualifier, use.Name) && !strings.EqualFold(it.qualifier, use.Alias) {
			return Source{}, fmt.Sprintf("column %d names another table", i+1)
		}
		stars = stars || it.star
	}
	if !stars {
		if len(items) != len(cols) {
			return Source{}, "the result does not match the select list"
		}
		for _, it := range items {
			src.Columns = append(src.Columns, it.column)
		}
	} else {
		for _, c := range cols {
			col := c
			for _, it := range items {
				if !it.star && strings.EqualFold(it.output, c) {
					col = it.column
				}
			}
			src.Columns = append(src.Columns, col)
		}
	}
	seen := map[string]bool{}
	for _, c := range src.Columns {
		k := strings.ToLower(c)
		if seen[k] {
			return Source{}, "column " + c + " appears twice"
		}
		seen[k] = true
	}
	return src, ""
}

// selectItems reads a select list made only of plain columns.
func selectItems(toks []token) ([]selectItem, string) {
	var items []selectItem
	var cur []token
	depth := 0
	flush := func() string {
		it, ok := readItem(cur)
		if !ok {
			return fmt.Sprintf("column %d is an expression", len(items)+1)
		}
		items = append(items, it)
		cur = nil
		return ""
	}
	for _, t := range toks {
		depth += depthDelta(t)
		if depth == 0 && isPunct(t, ",") {
			if why := flush(); why != "" {
				return nil, why
			}
			continue
		}
		cur = append(cur, t)
	}
	if why := flush(); why != "" {
		return nil, why
	}
	return items, ""
}

// readItem reads "*", "t.*", "col", "t.col", each but the stars optionally
// followed by [AS] alias.
func readItem(toks []token) (selectItem, bool) {
	var it selectItem
	j := 0
	switch {
	case len(toks) == 1 && isPunct(toks[0], "*"):
		return selectItem{star: true}, true
	case len(toks) == 3 && isName(toks[0]) && isPunct(toks[1], ".") && isPunct(toks[2], "*"):
		return selectItem{star: true, qualifier: toks[0].text}, true
	case len(toks) >= 3 && isName(toks[0]) && isPunct(toks[1], ".") && isName(toks[2]):
		it.qualifier, it.column, j = toks[0].text, toks[2].text, 3
	case len(toks) >= 1 && isName(toks[0]) && (toks[0].kind != tokWord || !IsNumber(toks[0].text)):
		it.column, j = toks[0].text, 1
	default:
		return it, false
	}
	it.output = it.column
	if j < len(toks) && isWord(toks[j], "AS") {
		j++
	}
	if j < len(toks) && isName(toks[j]) {
		it.output = toks[j].text
		j++
	}
	return it, j == len(toks)
}
