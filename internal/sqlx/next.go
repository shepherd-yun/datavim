package sqlx

import "strings"

// Next is what usually follows once a word is finished and a space typed:
// keywords, operators and "*" in the order they are offered, and whether
// names fit there too (the tables or columns of Complete's Kind).
type Next struct {
	Words []string
	Names bool
	Lower bool // the statement's keywords are written in lower case
}

var (
	// operandWords are keywords that end a value, like a name does.
	operandWords = wordSet("NULL", "TRUE", "FALSE", "CURRENT_TIMESTAMP", "CURRENT_DATE", "NOW")
	// nameAfter are keywords a name follows: a table or a column.
	nameAfter = wordSet("FROM", "JOIN", "UPDATE", "INTO", "TABLE", "WHERE", "AND", "OR", "ON", "HAVING", "BY",
		"SET", "WHEN", "THEN", "ELSE", "DISTINCT")
	// clauseWords are where Following looks back to for what an operand ends.
	clauseWords = wordSet("SELECT", "FROM", "JOIN", "WHERE", "ON", "HAVING", "BY", "SET", "UPDATE", "INTO",
		"LIMIT", "OFFSET", "VALUES", "USING")
	// testWords make a condition complete, like an operator does.
	testWords = wordSet("LIKE", "IN", "IS", "BETWEEN", "EXISTS", "REGEXP", "ILIKE")
	operators = wordSet("=", "<", ">", "!", "~")

	afterTableWords = []string{"WHERE", "JOIN", "LEFT JOIN", "INNER JOIN", "GROUP BY", "ORDER BY", "LIMIT"}
	testOperators   = []string{"=", "<>", "IN", "LIKE", "IS NULL", "IS NOT NULL", "BETWEEN"}
)

// Following tells what fits after the space before cursor, in the
// statement around it. Nothing is offered while a word is being typed or
// inside a string or comment.
func Following(src string, cursor int, d Dialect) Next {
	cursor = max(0, min(cursor, len(src)))
	if cursor == 0 || !isSpace(src[cursor-1]) {
		return Next{}
	}
	start, _ := statementAround(src, cursor, d)
	stmt := src[start:cursor]
	if insideLiteral(stmt, len(stmt), d) {
		return Next{}
	}
	toks := codeTokens(stmt, d)
	if len(toks) == 0 {
		return Next{}
	}
	out := following(toks, d)
	for i := len(toks) - 1; i >= 0; i-- {
		// a function such as count( is no keyword here
		if t := toks[i]; t.kind == tokWord && isKeyword(t.text, d) && (i+1 == len(toks) || !isPunct(toks[i+1], "(")) {
			out.Lower = t.text == strings.ToLower(t.text)
			break
		}
	}
	return out
}

func following(toks []token, d Dialect) Next {
	n := len(toks)
	last := toks[n-1]
	if last.kind == tokWord && isKeyword(last.text, d) && !operandWords[upper(last)] {
		return afterKeyword(toks)
	}
	if last.kind == tokPunct && last.text != ")" && last.text != "*" {
		switch c, _ := clauseOf(toks); {
		case last.text == ",":
			return Next{Names: true}
		case operators[last.text] && c == "ON":
			return Next{Names: true} // ON a.id = b.id
		}
		return Next{}
	}
	return afterOperand(toks)
}

// afterKeyword is what follows the keyword ending toks.
func afterKeyword(toks []token) Next {
	n := len(toks)
	switch w := upper(toks[n-1]); {
	case w == "SELECT":
		return Next{Words: []string{"*", "DISTINCT"}, Names: true}
	case w == "INSERT":
		return Next{Words: []string{"INTO"}}
	case w == "DELETE":
		return Next{Words: []string{"FROM"}}
	case w == "ORDER" || w == "GROUP":
		return Next{Words: []string{"BY"}}
	case w == "LEFT" || w == "RIGHT" || w == "FULL":
		return Next{Words: []string{"JOIN", "OUTER JOIN"}}
	case w == "INNER" || w == "CROSS" || w == "OUTER":
		return Next{Words: []string{"JOIN"}}
	case w == "IS":
		return Next{Words: []string{"NULL", "NOT NULL"}}
	case w == "NOT" && n >= 2 && isWord(toks[n-2], "IS"):
		return Next{Words: []string{"NULL"}}
	case w == "NOT":
		return Next{Words: []string{"IN", "LIKE", "BETWEEN", "EXISTS"}, Names: true}
	case w == "ASC" || w == "DESC":
		return Next{Words: []string{"LIMIT"}}
	case nameAfter[w]:
		return Next{Names: true}
	}
	return Next{}
}

// afterOperand is what follows the name, value or ")" ending toks.
func afterOperand(toks []token) Next {
	clause, at := clauseOf(toks)
	seg := toks[at+1:] // the operand's part of the clause
	for i := len(seg) - 1; i >= 0; i-- {
		if isPunct(seg[i], ",") {
			seg = seg[i+1:]
			break
		}
	}
	switch clause {
	case "SELECT":
		if n := len(seg); n >= 2 && isWord(seg[n-2], "AS") || isPunct(toks[len(toks)-1], "*") {
			return Next{Words: []string{"FROM"}}
		}
		return Next{Words: []string{"FROM", "AS"}}
	case "FROM":
		if isWord(toks[0], "DELETE") {
			return Next{Words: []string{"WHERE"}}
		}
		if aliased(seg) {
			return Next{Words: afterTableWords}
		}
		return Next{Words: append(append([]string(nil), afterTableWords...), "AS")}
	case "JOIN":
		if aliased(seg) {
			return Next{Words: []string{"ON", "USING"}}
		}
		return Next{Words: []string{"ON", "USING", "AS"}}
	case "UPDATE":
		return Next{Words: []string{"SET"}}
	case "INTO":
		return Next{Words: []string{"VALUES", "SELECT"}}
	case "SET":
		if tested(seg) {
			return Next{Words: []string{"WHERE"}}
		}
		return Next{Words: []string{"="}}
	case "LIMIT":
		return Next{Words: []string{"OFFSET"}}
	case "BY":
		if at > 0 && isWord(toks[at-1], "ORDER") {
			return Next{Words: []string{"ASC", "DESC", "LIMIT"}}
		}
		return Next{Words: []string{"HAVING", "ORDER BY", "LIMIT"}}
	case "WHERE", "ON", "HAVING":
		if !tested(condition(toks)) {
			return Next{Words: testOperators}
		}
		switch clause {
		case "WHERE":
			return Next{Words: []string{"AND", "OR", "GROUP BY", "ORDER BY", "LIMIT"}}
		case "ON":
			return Next{Words: []string{"AND", "OR", "WHERE", "JOIN", "LEFT JOIN", "GROUP BY", "ORDER BY", "LIMIT"}}
		}
		return Next{Words: []string{"AND", "OR", "ORDER BY", "LIMIT"}}
	}
	return Next{}
}

// clauseOf finds the clause word the end of toks is in, outside
// parentheses, and its index; "" when there is none before an opening
// parenthesis or the start.
func clauseOf(toks []token) (string, int) {
	depth := 0
	for i := len(toks) - 1; i >= 0; i-- {
		t := toks[i]
		switch {
		case isPunct(t, ")"):
			depth++
		case isPunct(t, "("):
			if depth == 0 {
				return "", i
			}
			depth--
		case depth == 0 && t.kind == tokWord && clauseWords[upper(t)]:
			return upper(t), i
		}
	}
	return "", -1
}

// condition is the last condition of toks: the tokens after the last AND,
// OR or clause word outside parentheses.
func condition(toks []token) []token {
	depth := 0
	for i := len(toks) - 1; i >= 0; i-- {
		t := toks[i]
		switch {
		case isPunct(t, ")"):
			depth++
		case isPunct(t, "("):
			if depth == 0 {
				return toks[i+1:]
			}
			depth--
		case depth == 0 && t.kind == tokWord && (clauseWords[upper(t)] || upper(t) == "AND" || upper(t) == "OR"):
			return toks[i+1:]
		}
	}
	return toks
}

// tested reports whether seg compares: an operator or a test word.
func tested(seg []token) bool {
	for _, t := range seg {
		if t.kind == tokPunct && operators[t.text] || t.kind == tokWord && testWords[upper(t)] {
			return true
		}
	}
	return false
}

// aliased reports whether the table reference seg has an alias: its last
// name follows another token that is not a qualifier's ".".
func aliased(seg []token) bool {
	n := len(seg)
	return n >= 2 && !isPunct(seg[n-2], ".")
}
