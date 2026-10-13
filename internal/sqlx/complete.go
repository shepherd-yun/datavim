package sqlx

import "strings"

// CompleteKind is what the word at the cursor should be completed with.
type CompleteKind int

const (
	CompleteNone CompleteKind = iota // inside a string, comment or quoted identifier
	CompleteKeyword
	CompleteTable
	CompleteColumn
)

// TableUse is a table a statement refers to.
type TableUse struct {
	Qualifier string // database (MySQL) or schema (PostgreSQL) written before the name
	Name      string
	Alias     string
}

// Completion describes the word under the cursor. Completing it replaces
// the source from Start to the cursor.
type Completion struct {
	Kind      CompleteKind
	Prefix    string // the part of the word before the cursor
	Start     int    // byte offset of the word in the source
	Qualifier string // the name before ".", if any: alias, table, schema or database
	Tables    []TableUse
}

var (
	tableClause   = wordSet("FROM", "JOIN", "UPDATE", "INTO", "TABLE")
	columnClause  = wordSet("SELECT", "WHERE", "ON", "SET", "BY", "HAVING", "AND", "OR", "NOT", "WHEN", "THEN", "ELSE", "DISTINCT", "USING")
	keywordClause = wordSet("VALUES", "LIMIT", "OFFSET")
	// aliasStop ends a table reference: these words are never aliases.
	aliasStop = wordSet("WHERE", "JOIN", "ON", "USING", "LEFT", "RIGHT", "INNER", "OUTER", "CROSS", "FULL",
		"NATURAL", "GROUP", "ORDER", "LIMIT", "HAVING", "UNION", "SET", "VALUES", "SELECT", "WINDOW", "FOR",
		"OFFSET", "FETCH", "INTERSECT", "EXCEPT", "LATERAL", "STRAIGHT_JOIN", "PARTITION", "USE", "FORCE",
		"IGNORE", "RETURNING", "DEFAULT", "AS")
)

// Complete inspects src at cursor (a byte offset).
func Complete(src string, cursor int, d Dialect) Completion {
	cursor = max(0, min(cursor, len(src)))
	start, end := statementAround(src, cursor, d)
	stmt, c := src[start:end], cursor-start
	if insideLiteral(stmt, c, d) {
		return Completion{Kind: CompleteNone, Start: cursor}
	}
	ws := c
	for ws > 0 && isWordByte(stmt[ws-1]) {
		ws--
	}
	out := Completion{Prefix: stmt[ws:c], Start: start + ws, Tables: tableUses(codeTokens(stmt, d))}
	before := codeTokens(stmt[:ws], d)
	if n := len(before); n >= 2 && isPunct(before[n-1], ".") && isName(before[n-2]) {
		out.Qualifier = before[n-2].text
		out.Kind = clauseKind(before[:n-2])
		if out.Kind == CompleteKeyword {
			out.Kind = CompleteColumn // "x." outside a known clause: x is most likely a table
		}
		return out
	}
	if n := len(before); n > 0 && endsOperand(before[n-1], d) {
		out.Kind = CompleteKeyword
		return out
	}
	out.Kind = clauseKind(before)
	return out
}

// statementAround returns the statement the cursor is in. Split trims
// trailing whitespace, so a cursor after a statement's last word (and only
// whitespace) still belongs to it; after a delimiter a new, empty
// statement starts at the cursor.
func statementAround(src string, cursor int, d Dialect) (start, end int) {
	stmts := Split(src, d)
	for i := len(stmts) - 1; i >= 0; i-- {
		s := stmts[i]
		switch {
		case s.Start > cursor:
			continue
		case cursor <= s.End:
			return s.Start, s.End
		case strings.TrimSpace(src[s.End:cursor]) == "":
			return s.Start, cursor
		}
		return cursor, cursor
	}
	return cursor, cursor
}

// insideLiteral reports whether offset c of src is inside a string, a
// quoted identifier or a comment.
func insideLiteral(src string, c int, d Dialect) bool {
	for i := 0; i < len(src) && i < c; {
		if isSpace(src[i]) {
			i++
			continue
		}
		t := lex(src, i, d)
		if t.kind == tokString || t.kind == tokIdent || t.kind == tokComment {
			if c > t.pos && c < t.end || c == t.end && !closed(src[t.pos:t.end]) {
				return true
			}
		}
		i = t.end
	}
	return false
}

// closed reports whether a string, quoted identifier or comment ends with
// its closing delimiter. A line comment runs to the end of its line, so a
// cursor at its end is still inside it.
func closed(raw string) bool {
	switch {
	case strings.HasPrefix(raw, "--"), strings.HasPrefix(raw, "#"):
		return false
	case strings.HasPrefix(raw, "/*"):
		return len(raw) >= 4 && strings.HasSuffix(raw, "*/")
	case strings.HasPrefix(raw, "$"):
		tag := raw[:strings.IndexByte(raw[1:], '$')+2]
		return len(raw) >= 2*len(tag) && strings.HasSuffix(raw, tag)
	case raw[0] == 'E' || raw[0] == 'e':
		raw = raw[1:]
	}
	return len(raw) >= 2 && raw[len(raw)-1] == raw[0]
}

// endsOperand reports whether t ends a value or name, after which the next
// word is most likely a keyword ("SELECT id |", "FROM users |").
func endsOperand(t token, d Dialect) bool {
	switch t.kind {
	case tokIdent, tokString:
		return true
	case tokPunct:
		return t.text == ")" || t.text == "*"
	case tokWord:
		return !isKeyword(t.text, d)
	}
	return false
}

// clauseKind looks back for the clause the cursor is in.
func clauseKind(toks []token) CompleteKind {
	for i := len(toks) - 1; i >= 0; i-- {
		if toks[i].kind != tokWord {
			continue
		}
		switch w := upper(toks[i]); {
		case tableClause[w]:
			if w == "INTO" && hasPunct(toks[i+1:], "(") {
				return CompleteColumn // INSERT INTO t (col, ...
			}
			return CompleteTable
		case columnClause[w]:
			return CompleteColumn
		case keywordClause[w]:
			return CompleteKeyword
		}
	}
	return CompleteKeyword
}

func hasPunct(toks []token, p string) bool {
	for _, t := range toks {
		if isPunct(t, p) {
			return true
		}
	}
	return false
}

// tableUses lists the tables after FROM (and its comma list), JOIN, UPDATE
// and INTO.
func tableUses(toks []token) []TableUse {
	var out []TableUse
	for i, t := range toks {
		if t.kind != tokWord {
			continue
		}
		w := upper(t)
		if w != "FROM" && w != "JOIN" && w != "UPDATE" && w != "INTO" {
			continue
		}
		j := i + 1
		for {
			u, next, ok := tableUse(toks, j)
			if !ok {
				break
			}
			out = append(out, u)
			if w != "FROM" || next >= len(toks) || !isPunct(toks[next], ",") {
				break
			}
			j = next + 1
		}
	}
	return out
}

func stopWord(t token) bool { return t.kind == tokWord && aliasStop[upper(t)] }

// tableUse reads "[qualifier.]name [[AS] alias]" at j.
func tableUse(toks []token, j int) (TableUse, int, bool) {
	for j < len(toks) && toks[j].kind == tokWord && modifiers[upper(toks[j])] {
		j++
	}
	if j >= len(toks) || !isName(toks[j]) || stopWord(toks[j]) {
		return TableUse{}, j, false
	}
	parts := []string{toks[j].text}
	j++
	for j+1 < len(toks) && isPunct(toks[j], ".") && isName(toks[j+1]) {
		parts = append(parts, toks[j+1].text)
		j += 2
	}
	u := TableUse{Name: parts[len(parts)-1]}
	if len(parts) > 1 {
		u.Qualifier = parts[len(parts)-2]
	}
	if j < len(toks) && isWord(toks[j], "AS") {
		j++
	}
	if j < len(toks) && isName(toks[j]) && !stopWord(toks[j]) {
		u.Alias = toks[j].text
		j++
	}
	return u, j, true
}
