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
