package sqlx

import "strings"

// IsComplete reports whether src can be handed to the sqlite3 shell without
// it waiting for more input once a final newline and semicolon are added: no
// string, quoted name or block comment is left open, and every CREATE TRIGGER
// has reached its "; END". It is what sqlite3_complete answers, except that a
// last statement needs no semicolon.
func IsComplete(src string, d Dialect) bool {
	for i := 0; i < len(src); {
		if isSpace(src[i]) {
			i++
			continue
		}
		t := lex(src, i, d)
		if !terminated(src[t.pos:t.end], t.kind) {
			return false
		}
		i = t.end
	}
	for _, s := range Split(src, d) {
		if toks := codeTokens(s.Text, d); isTriggerStart(toks) && !endsTrigger(toks) {
			return false
		}
	}
	return true
}

// terminated reports whether a token's source ends with its closing
// delimiter. Line comments end by themselves.
func terminated(raw string, kind tokKind) bool {
	switch kind {
	case tokComment:
		return !strings.HasPrefix(raw, "/*") || len(raw) >= 4 && strings.HasSuffix(raw, "*/")
	case tokString, tokIdent:
	default:
		return true
	}
	if len(raw) > 1 && (raw[0] == 'x' || raw[0] == 'X') && raw[1] == '\'' {
		raw = raw[1:]
	}
	switch raw[0] {
	case '[':
		return strings.HasSuffix(raw, "]")
	case '\'', '"', '`':
		return strings.Count(raw, raw[:1])%2 == 0 // the quotes inside are doubled
	}
	return true
}

// triggerBlock reads a CREATE TRIGGER starting at i. end is where its text
// stops (after END), next where scanning goes on (after the semicolon); a
// trigger without its "; END ;" runs to the end of src.
func triggerBlock(src string, i int) (end, next int, ok bool) {
	var head []token
	for j := i; j < len(src) && len(head) < 4; {
		if isSpace(src[j]) {
			j++
			continue
		}
		t := lex(src, j, SQLite)
		j = t.end
		if t.kind == tokComment {
			continue
		}
		if t.kind != tokWord {
			break
		}
		head = append(head, t)
	}
	if !isTriggerStart(head) {
		return 0, 0, false
	}
	var prev, before token
	for j := i; j < len(src); {
		if isSpace(src[j]) {
			j++
			continue
		}
		t := lex(src, j, SQLite)
		j = t.end
		if t.kind == tokComment {
			continue
		}
		if isPunct(t, ";") && isWord(prev, "END") && isPunct(before, ";") {
			return t.pos, t.end, true
		}
		prev, before = t, prev
	}
	return len(src), len(src), true
}

// isTriggerStart reports the first words of [EXPLAIN] CREATE [TEMP|TEMPORARY] TRIGGER.
func isTriggerStart(toks []token) bool {
	if len(toks) > 0 && isWord(toks[0], "EXPLAIN") {
		toks = toks[1:]
	}
	if len(toks) == 0 || !isWord(toks[0], "CREATE") {
		return false
	}
	toks = toks[1:]
	if len(toks) > 0 && (isWord(toks[0], "TEMP") || isWord(toks[0], "TEMPORARY")) {
		toks = toks[1:]
	}
	return len(toks) > 0 && isWord(toks[0], "TRIGGER")
}

func endsTrigger(toks []token) bool {
	n := len(toks)
	return n >= 2 && isWord(toks[n-1], "END") && isPunct(toks[n-2], ";")
}

// CallsFunction returns the first of names (compared without case) that stmt
// calls: a name, bare or quoted, followed by "(", comments aside. A table or
// column of that name followed by "(" counts too.
func CallsFunction(stmt string, d Dialect, names ...string) (string, bool) {
	toks := codeTokens(stmt, d)
	for i := 0; i+1 < len(toks); i++ {
		if !isName(toks[i]) || !isPunct(toks[i+1], "(") {
			continue
		}
		for _, n := range names {
			if strings.EqualFold(toks[i].text, n) {
				return n, true
			}
		}
	}
	return "", false
}

var (
	// pragmaInspect only read, whatever their argument names.
	pragmaInspect = wordSet("TABLE_INFO", "TABLE_XINFO", "TABLE_LIST", "INDEX_LIST", "INDEX_INFO", "INDEX_XINFO",
		"FOREIGN_KEY_LIST", "FOREIGN_KEY_CHECK", "INTEGRITY_CHECK", "QUICK_CHECK", "COLLATION_LIST", "COMPILE_OPTIONS",
		"DATABASE_LIST", "FUNCTION_LIST", "MODULE_LIST", "PRAGMA_LIST", "FREELIST_COUNT", "PAGE_COUNT", "DATA_VERSION", "STATS")
	// pragmaState read a setting when they have no argument and change it when they have one.
	pragmaState = wordSet("APPLICATION_ID", "AUTO_VACUUM", "AUTOMATIC_INDEX", "BUSY_TIMEOUT", "CACHE_SIZE", "CACHE_SPILL",
		"CELL_SIZE_CHECK", "CHECKPOINT_FULLFSYNC", "DEFER_FOREIGN_KEYS", "ENCODING", "FOREIGN_KEYS", "FULLFSYNC",
		"IGNORE_CHECK_CONSTRAINTS", "JOURNAL_MODE", "JOURNAL_SIZE_LIMIT", "LEGACY_ALTER_TABLE", "LOCKING_MODE",
		"MAX_PAGE_COUNT", "MMAP_SIZE", "PAGE_SIZE", "QUERY_ONLY", "READ_UNCOMMITTED", "RECURSIVE_TRIGGERS",
		"REVERSE_UNORDERED_SELECTS", "SCHEMA_VERSION", "SECURE_DELETE", "SOFT_HEAP_LIMIT", "SYNCHRONOUS", "TEMP_STORE",
		"THREADS", "USER_VERSION", "WAL_AUTOCHECKPOINT")
)

// analyzePragma classifies PRAGMA [schema.]name [= value | (value)]: a read
// only when the pragma is known to be one. Every pragma may answer with rows.
func analyzePragma(toks []token) Info {
	name, next := identPath(toks, 1)
	name = strings.ToUpper(name)
	switch {
	case next < len(toks) && isPunct(toks[next], ";"):
		toks = toks[:next]
	case next < len(toks) && isPunct(toks[len(toks)-1], ";"):
		toks = toks[:len(toks)-1]
	}
	hasArg := next < len(toks)
	callForm := hasArg && isPunct(toks[next], "(")
	if pragmaState[name] && !hasArg || pragmaInspect[name] && (!hasArg || callForm) {
		return Info{Kind: Read, ReturnsRows: true}
	}
	return Info{Kind: Write, ReturnsRows: true}
}

// Placeholders are the byte offsets of the ? parameters of stmt, outside
// strings, names and comments.
func Placeholders(stmt string, d Dialect) []int {
	var out []int
	for _, t := range codeTokens(stmt, d) {
		if isPunct(t, "?") {
			out = append(out, t.pos)
		}
	}
	return out
}

// FindToken is the offset of the first token of stmt spelled text: how a
// message like near "x" is placed, as the engine names the token but not
// where it is.
func FindToken(stmt string, d Dialect, text string) (int, bool) {
	for _, t := range codeTokens(stmt, d) {
		if stmt[t.pos:t.end] == text {
			return t.pos, true
		}
	}
	return 0, false
}

// SQLiteColumn is what a column definition says beyond PRAGMA table_xinfo.
type SQLiteColumn struct {
	Collation     string // upper case
	Generated     string
	AutoIncrement bool
}

// SQLiteTable is what a CREATE TABLE says beyond the pragmas.
type SQLiteTable struct {
	Columns     map[string]SQLiteColumn // by lower-case name
	PrimaryName string                  // CONSTRAINT name of the primary key
	ForeignKeys map[string]string       // constraint names by their lower-case columns, joined by commas
}

// ParseSQLiteTable reads a CREATE TABLE as sqlite_master holds it; the text
// is known to be valid. CREATE TABLE … AS has no column definitions.
func ParseSQLiteTable(src string) SQLiteTable {
	out := SQLiteTable{}
	toks := codeTokens(src, SQLite)
	if depth0Word(toks, 0, "AS") {
		return out
	}
	for _, item := range parenItems(toks) {
		if len(item) == 0 {
			continue
		}
		if name := item[0]; name.kind == tokIdent || !tableConstraintWord(name) {
			parseColumnDef(src, item, &out)
		} else {
			parseTableConstraint(item, &out)
		}
	}
	return out
}

func tableConstraintWord(t token) bool {
	switch upper(t) {
	case "CONSTRAINT", "PRIMARY", "UNIQUE", "CHECK", "FOREIGN":
		return true
	}
	return false
}

// parenItems are the comma-separated items of the first parenthesis.
func parenItems(toks []token) [][]token {
	open := -1
	for i, t := range toks {
		if isPunct(t, "(") {
			open = i
			break
		}
	}
	if open < 0 {
		return nil
	}
	var out [][]token
	var cur []token
	depth := 1
	for _, t := range toks[open+1:] {
		depth += depthDelta(t)
		if depth == 0 {
			break
		}
		if depth == 1 && isPunct(t, ",") {
			out = append(out, cur)
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	return append(out, cur)
}

func parseColumnDef(src string, item []token, out *SQLiteTable) {
	key := strings.ToLower(item[0].text)
	var c SQLiteColumn
	constraint := ""
	depth := 0
	for i := 1; i < len(item); i++ {
		t := item[i]
		depth += depthDelta(t)
		if depth != 0 || t.kind != tokWord && !isPunct(t, "(") {
			continue
		}
		next := func() string {
			if i+1 < len(item) {
				return item[i+1].text
			}
			return ""
		}
		switch upper(t) {
		case "CONSTRAINT":
			constraint = next()
			i++
		case "COLLATE":
			c.Collation = strings.ToUpper(next())
			i++
		case "AUTOINCREMENT":
			c.AutoIncrement = true
		case "PRIMARY":
			if constraint != "" {
				out.PrimaryName = constraint
			}
		case "REFERENCES":
			if constraint != "" {
				if out.ForeignKeys == nil {
					out.ForeignKeys = map[string]string{}
				}
				out.ForeignKeys[key] = constraint
			}
		case "AS":
			if i+1 < len(item) && isPunct(item[i+1], "(") {
				c.Generated = parenText(src, item[i+1:])
			}
		}
	}
	if out.Columns == nil {
		out.Columns = map[string]SQLiteColumn{}
	}
	out.Columns[key] = c
}

func parseTableConstraint(item []token, out *SQLiteTable) {
	constraint := ""
	if isWord(item[0], "CONSTRAINT") && len(item) > 2 {
		constraint = item[1].text
		item = item[2:]
	}
	switch {
	case constraint == "":
	case isWord(item[0], "PRIMARY"):
		out.PrimaryName = constraint
	case isWord(item[0], "FOREIGN"):
		var cols []string
		for _, t := range item[1:] {
			if isPunct(t, ")") {
				break
			}
			if isName(t) && !isWord(t, "KEY") {
				cols = append(cols, strings.ToLower(t.text))
			}
		}
		if out.ForeignKeys == nil {
			out.ForeignKeys = map[string]string{}
		}
		out.ForeignKeys[strings.Join(cols, ",")] = constraint
	}
}

// parenText is the source between the parenthesis at toks[0] and its partner.
func parenText(src string, toks []token) string {
	depth := 0
	for _, t := range toks {
		depth += depthDelta(t)
		if depth == 0 {
			return strings.TrimSpace(src[toks[0].end:t.pos])
		}
	}
	return ""
}

// ParseSQLiteIndex reads a CREATE INDEX as sqlite_master holds it: the text
// of its key entries, what follows each of them (COLLATE, ASC, DESC), and its
// WHERE condition.
func ParseSQLiteIndex(src string) (entries, suffixes []string, where string) {
	toks := codeTokens(src, SQLite)
	depth := 0
	for i, t := range toks {
		depth += depthDelta(t)
		if depth == 0 && isWord(t, "WHERE") && i+1 < len(toks) {
			where = strings.TrimSpace(src[toks[i+1].pos:])
			break
		}
	}
	for _, item := range parenItems(toks) {
		if len(item) == 0 {
			continue
		}
		last, suffix := len(item), ""
		nested := 0
		for k, t := range item {
			nested += depthDelta(t)
			if k > 0 && nested == 0 && (isWord(t, "COLLATE") || isWord(t, "ASC") || isWord(t, "DESC")) {
				last = k
				suffix = src[t.pos:item[len(item)-1].end]
				break
			}
		}
		entries = append(entries, src[item[0].pos:item[last-1].end])
		suffixes = append(suffixes, suffix)
	}
	return entries, suffixes, where
}

// LeadingWords are the first n tokens of stmt in upper case, comments aside.
func LeadingWords(stmt string, d Dialect, n int) []string {
	toks := codeTokens(stmt, d)
	out := make([]string, 0, n)
	for i := 0; i < n && i < len(toks); i++ {
		out = append(out, upper(toks[i]))
	}
	return out
}

// TerminatorLine reports a line of stmt holding only "/" or "go", comments
// aside: for the sqlite3 shell such a line ends a complete statement, so what
// follows it starts afresh and may be taken for a dot command.
func TerminatorLine(stmt string, d Dialect) bool {
	toks := codeTokens(stmt, d)
	for i, t := range toks {
		if !isPunct(t, "/") && !isWord(t, "go") {
			continue
		}
		before := i == 0 || strings.Contains(stmt[toks[i-1].end:t.pos], "\n")
		after := i == len(toks)-1 || strings.Contains(stmt[t.end:toks[i+1].pos], "\n")
		if before && after {
			return true
		}
	}
	return false
}

// RewriteStrings replaces the string literals of stmt with what fn makes of them.
func RewriteStrings(stmt string, d Dialect, fn func(raw string) string) string {
	var b strings.Builder
	last := 0
	for _, t := range codeTokens(stmt, d) {
		if t.kind == tokString {
			b.WriteString(stmt[last:t.pos])
			b.WriteString(fn(stmt[t.pos:t.end]))
			last = t.end
		}
	}
	b.WriteString(stmt[last:])
	return b.String()
}
