package sqlx

import "strings"

// Kind classifies a statement for the safety checks.
type Kind int

const (
	Other Kind = iota
	Read
	Write // DML, CALL, COPY, SELECT ... INTO
	DDL
	Tx
)

func (k Kind) String() string {
	return [...]string{"other", "read", "write", "ddl", "transaction"}[k]
}

// Info describes one statement.
type Info struct {
	Kind         Kind
	ReturnsRows  bool   // run it as a query rather than an exec
	MissingWhere bool   // an UPDATE or DELETE without a WHERE clause
	Target       string // the table written to (or read by a single-table SELECT)
	SQL          string // AnalyzeBatch: the statement's text within the batch
}

func wordSet(ws ...string) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[w] = true
	}
	return m
}

var (
	readWords  = wordSet("SHOW", "DESCRIBE", "DESC", "VALUES", "TABLE")
	writeWords = wordSet("LOAD", "COPY", "DO")
	ddlWords   = wordSet("CREATE", "ALTER", "DROP", "TRUNCATE", "RENAME", "COMMENT", "GRANT", "REVOKE", "REINDEX", "CLUSTER", "VACUUM")
	txWords    = wordSet("BEGIN", "START", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE", "END", "ABORT")
	dmlWords   = wordSet("INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE")
	mainWords  = wordSet("SELECT", "INSERT", "UPDATE", "DELETE", "MERGE", "REPLACE", "VALUES", "TABLE")
	modifiers  = wordSet("LOW_PRIORITY", "HIGH_PRIORITY", "DELAYED", "QUICK", "IGNORE", "ONLY")
	notDMLPrev = wordSet("FOR", "ON", "KEY")
	clauseEnd  = wordSet("WHERE", "GROUP", "ORDER", "LIMIT", "HAVING", "UNION", "WINDOW", "FOR", "OFFSET", "FETCH", "INTERSECT", "EXCEPT")
)

// Analyze classifies one statement (as produced by Split).
func Analyze(stmt string, d Dialect) Info {
	toks := codeTokens(stmt, d)
	i := 0
	for i < len(toks) && isPunct(toks[i], "(") {
		i++
	}
	return classify(toks, i)
}

func classify(toks []token, i int) Info {
	if i >= len(toks) || toks[i].kind != tokWord {
		return Info{}
	}
	switch first := upper(toks[i]); {
	case first == "WITH":
		return analyzeWith(toks, i)
	case first == "EXPLAIN":
		info := Info{Kind: Read, ReturnsRows: true}
		if k := indexWord(toks, i+1, "ANALYZE"); k >= 0 {
			if m := indexIn(toks, k+1, dmlWords); m >= 0 {
				inner := classify(toks, m)
				info.Kind, info.MissingWhere, info.Target = Write, inner.MissingWhere, inner.Target
			}
		}
		return info
	case first == "SELECT":
		if depth0Word(toks, i, "INTO") {
			return Info{Kind: Write}
		}
		return Info{Kind: Read, ReturnsRows: true, Target: selectTarget(toks, i)}
	case readWords[first]:
		return Info{Kind: Read, ReturnsRows: true}
	case dmlWords[first]:
		return dml(toks, i, first)
	case first == "CALL":
		return Info{Kind: Write, ReturnsRows: true}
	case writeWords[first]:
		return Info{Kind: Write}
	case ddlWords[first]:
		return Info{Kind: DDL}
	case txWords[first]:
		return Info{Kind: Tx}
	}
	return Info{}
}

func dml(toks []token, i int, kw string) Info {
	info := Info{Kind: Write, ReturnsRows: depth0Word(toks, i, "RETURNING")}
	switch kw {
	case "INSERT", "REPLACE", "MERGE":
		k := indexWord(toks, i+1, "INTO")
		if k < 0 {
			k = i
		}
		info.Target = identAfter(toks, k)
	case "UPDATE":
		info.Target = identAfter(toks, i)
		info.MissingWhere = missingWhere(toks, i)
	case "DELETE":
		if k := depth0Index(toks, i, "FROM"); k >= 0 {
			info.Target = identAfter(toks, k)
		}
		info.MissingWhere = missingWhere(toks, i)
	}
	return info
}

func analyzeWith(toks []token, i int) Info {
	depth := 0
	main := -1
	for j := i + 1; j < len(toks) && main < 0; j++ {
		depth += depthDelta(toks[j])
		if depth == 0 && toks[j].kind == tokWord && mainWords[upper(toks[j])] {
			main = j
		}
	}
	if main < 0 {
		return Info{}
	}
	info := classify(toks, main)
	if !dmlWords[upper(toks[main])] {
		info.Target = "" // FROM may name a CTE rather than a table
	}
	for j := i + 1; j < main; j++ {
		if !isDML(toks, j) {
			continue
		}
		info.Kind = Write
		if w := upper(toks[j]); (w == "UPDATE" || w == "DELETE") && missingWhere(toks, j) && !info.MissingWhere {
			inner := dml(toks, j, w)
			info.MissingWhere, info.Target = true, inner.Target
		}
	}
	return info
}

// missingWhere reports whether the UPDATE/DELETE at k has no WHERE at its own
// parenthesis level before that level closes.
func missingWhere(toks []token, k int) bool {
	depth := 0
	for j := k + 1; j < len(toks); j++ {
		depth += depthDelta(toks[j])
		if depth < 0 {
			return true
		}
		if depth == 0 && isWord(toks[j], "WHERE") {
			return false
		}
	}
	return true
}

func selectTarget(toks []token, i int) string {
	k := depth0Index(toks, i, "FROM")
	if k < 0 {
		return ""
	}
	name, next := identPath(toks, k+1)
	depth := 0
	for j := next; j < len(toks); j++ {
		depth += depthDelta(toks[j])
		if depth != 0 {
			continue
		}
		if toks[j].kind == tokWord && clauseEnd[upper(toks[j])] {
			break
		}
		if isWord(toks[j], "JOIN") || isPunct(toks[j], ",") {
			return ""
		}
	}
	return name
}

// identAfter skips modifiers after toks[k] and returns the table name there.
func identAfter(toks []token, k int) string {
	j := k + 1
	for j < len(toks) && toks[j].kind == tokWord && modifiers[upper(toks[j])] {
		j++
	}
	name, _ := identPath(toks, j)
	return name
}

// identPath reads a dotted name at j and returns its last part and the index after it.
func identPath(toks []token, j int) (string, int) {
	if j >= len(toks) || !isName(toks[j]) {
		return "", j
	}
	last := toks[j].text
	j++
	for j+1 < len(toks) && isPunct(toks[j], ".") && isName(toks[j+1]) {
		last = toks[j+1].text
		j += 2
	}
	return last, j
}

func isName(t token) bool { return t.kind == tokWord || t.kind == tokIdent }

func isDML(toks []token, j int) bool {
	if toks[j].kind != tokWord || !dmlWords[upper(toks[j])] {
		return false
	}
	return j == 0 || toks[j-1].kind != tokWord || !notDMLPrev[upper(toks[j-1])]
}

func depthDelta(t token) int {
	switch {
	case isPunct(t, "("):
		return 1
	case isPunct(t, ")"):
		return -1
	}
	return 0
}

func depth0Index(toks []token, from int, w string) int {
	depth := 0
	for j := from; j < len(toks); j++ {
		depth += depthDelta(toks[j])
		if depth == 0 && isWord(toks[j], w) {
			return j
		}
	}
	return -1
}

func depth0Word(toks []token, from int, w string) bool { return depth0Index(toks, from, w) >= 0 }

func indexWord(toks []token, from int, w string) int {
	for j := from; j < len(toks); j++ {
		if isWord(toks[j], w) {
			return j
		}
	}
	return -1
}

func indexIn(toks []token, from int, set map[string]bool) int {
	for j := from; j < len(toks); j++ {
		if toks[j].kind == tokWord && set[upper(toks[j])] {
			return j
		}
	}
	return -1
}

func upper(t token) string           { return strings.ToUpper(t.text) }
func isWord(t token, w string) bool  { return t.kind == tokWord && strings.EqualFold(t.text, w) }
func isPunct(t token, p string) bool { return t.kind == tokPunct && t.text == p }

// UnlocksReadOnly reports whether stmt may switch the session back to
// read-write (SET … read_only, TRANSACTION READ WRITE, set_config(…)), which
// would switch off the server-side guard of a read-only connection. The
// read_only check looks at the raw text so names inside strings count too.
func UnlocksReadOnly(stmt string, d Dialect) bool {
	if strings.Contains(strings.ToLower(stmt), "read_only") {
		return true
	}
	toks := codeTokens(stmt, d)
	for i := 1; i < len(toks); i++ {
		if toks[i-1].kind == tokWord && toks[i].kind == tokWord &&
			strings.EqualFold(toks[i-1].text, "read") && strings.EqualFold(toks[i].text, "write") {
			return true
		}
	}
	return false
}

var (
	// batchStart are the words that start a statement inside a batch, where
	// a statement needs no semicolon (T-SQL) or sits in a block (PL/SQL).
	batchStart = wordSet("INSERT", "UPDATE", "DELETE", "MERGE", "REPLACE",
		"CREATE", "ALTER", "DROP", "TRUNCATE", "RENAME", "GRANT", "REVOKE", "DENY",
		"EXEC", "EXECUTE", "CALL", "KILL", "SHUTDOWN", "DBCC", "BACKUP", "RESTORE", "RECONFIGURE", "BULK")
	// notBatchPrev are words after which those words continue a statement:
	// FOR UPDATE, ON DELETE, GRANT INSERT, AFTER INSERT, …
	notBatchPrev = wordSet("FOR", "ON", "KEY", "OF", "AFTER", "BEFORE", "INSTEAD", "GRANT", "REVOKE", "DENY", "BULK")
)

// AnalyzeBatch classifies every statement in a T-SQL batch or PL/SQL block:
// stmt may hold several statements without semicolons. It cuts stmt before
// each top-level word that starts a write, a DDL statement or a procedure
// call (outside strings and comments) and after each semicolon, and
// returns one Info per part, with SQL set to the part's text. A MERGE keeps
// its WHEN … THEN UPDATE/INSERT/DELETE.
func AnalyzeBatch(stmt string, d Dialect) []Info {
	toks := codeTokens(stmt, d)
	if len(toks) == 0 {
		return []Info{{SQL: stmt}}
	}
	var out []Info
	from := 0
	cut := func(to int) {
		if from >= to {
			return
		}
		end := len(stmt)
		if to < len(toks) {
			end = toks[to].pos
		}
		text := strings.TrimSpace(stmt[toks[from].pos:end])
		info := Analyze(text, d)
		info.SQL = text
		out = append(out, info)
		from = to
	}
	depth := 0
	for j, t := range toks {
		depth += depthDelta(t)
		switch {
		case j > 0 && isPunct(toks[j-1], ";"):
			cut(j)
		case depth != 0 || j == from || t.kind != tokWord || !batchStart[upper(t)]:
		case toks[j-1].kind == tokPunct && (toks[j-1].text == "." || toks[j-1].text == ","):
		case toks[j-1].kind == tokWord && notBatchPrev[upper(toks[j-1])]:
		case isWord(toks[j-1], "THEN") && isWord(toks[from], "MERGE"):
		default:
			cut(j)
		}
	}
	cut(len(toks))
	return out
}

// FirstWord is the first token of stmt, comments aside.
func FirstWord(stmt string, d Dialect) string {
	if toks := codeTokens(stmt, d); len(toks) > 0 {
		return toks[0].text
	}
	return ""
}

// tsqlSafeProcs are the system procedures a read-only SQL Server
// connection may call: they only read.
var tsqlSafeProcs = wordSet("SP_HELP", "SP_HELPTEXT", "SP_HELPINDEX", "SP_COLUMNS", "SP_TABLES", "SP_WHO",
	"SP_WHO2", "SP_SPACEUSED", "SP_DATABASES", "SP_SERVER_INFO")

// SafeOther reports a statement of Kind Other that cannot write: the
// dialect's allowlist for engines without a server-side read-only mode.
// MySQL and PostgreSQL have none.
func SafeOther(stmt string, d Dialect) bool {
	toks := codeTokens(stmt, d)
	if len(toks) == 0 {
		return true
	}
	//exhaustive:enforce
	switch d {
	case SQLServer:
		switch upper(toks[0]) {
		case "USE", "SET", "DECLARE", "PRINT":
			return true
		case "EXEC", "EXECUTE":
			name, next := identPath(toks, 1)
			qualified := next > 2
			if qualified && (next != 4 || !isWord(toks[1], "sys")) {
				return false
			}
			return tsqlSafeProcs[strings.ToUpper(name)]
		}
	case MySQL, Postgres, Oracle:
	}
	return false
}
