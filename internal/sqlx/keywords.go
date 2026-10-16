package sqlx

import (
	"slices"
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
	sqlserverKeywords = []string{
		"APPLY", "CATCH", "CONVERT", "DATEADD", "DATEDIFF", "DECLARE", "EXEC", "EXECUTE", "FETCH",
		"GETDATE", "GO", "IDENTITY", "IF", "ISNULL", "MERGE", "NEXT", "NOLOCK", "NVARCHAR", "OFFSET",
		"ONLY", "OUTPUT", "PIVOT", "PRINT", "RETURN", "ROWS", "SAVE", "TOP", "TRAN", "TRY",
		"UNPIVOT", "USE", "WHILE",
	}
	oracleKeywords = []string{
		"CONNECT", "DECODE", "DUAL", "FETCH", "FIRST", "LEVEL", "MERGE", "MINUS", "NEXT", "NOCOPY",
		"NVL", "NVL2", "OFFSET", "ONLY", "PRIOR", "ROWID", "ROWNUM", "ROWS", "START", "SYSDATE",
		"SYSTIMESTAMP", "TO_CHAR", "TO_DATE", "TO_NUMBER", "VARCHAR2", "NUMBER",
	}
	keywordLists = map[Dialect][]string{
		MySQL:     sortedUnique(commonKeywords, mysqlKeywords),
		Postgres:  sortedUnique(commonKeywords, postgresKeywords),
		SQLServer: without(sortedUnique(commonKeywords, sqlserverKeywords), "LIMIT"),
		Oracle:    without(sortedUnique(commonKeywords, oracleKeywords), "LIMIT"),
	}
	keywordSets = map[Dialect]map[string]bool{
		MySQL:     wordSet(keywordLists[MySQL]...),
		Postgres:  wordSet(keywordLists[Postgres]...),
		SQLServer: wordSet(keywordLists[SQLServer]...),
		Oracle:    wordSet(keywordLists[Oracle]...),
	}
	// reserved holds the words each dialect does not accept as bare
	// identifiers: MySQL 8.0's reserved words and PostgreSQL's reserved ones
	// (including those that may only be function or type names).
	reserved = map[Dialect]map[string]bool{
		MySQL: wordSet(
			"ACCESSIBLE", "ADD", "ALL", "ALTER", "ANALYZE", "AND", "AS", "ASC", "ASENSITIVE", "BEFORE",
			"BETWEEN", "BIGINT", "BINARY", "BLOB", "BOTH", "BY", "CALL", "CASCADE", "CASE", "CHANGE",
			"CHAR", "CHARACTER", "CHECK", "COLLATE", "COLUMN", "CONDITION", "CONSTRAINT", "CONTINUE",
			"CONVERT", "CREATE", "CROSS", "CUBE", "CUME_DIST", "CURRENT_DATE", "CURRENT_TIME",
			"CURRENT_TIMESTAMP", "CURRENT_USER", "CURSOR", "DATABASE", "DATABASES", "DAY_HOUR",
			"DAY_MICROSECOND", "DAY_MINUTE", "DAY_SECOND", "DEC", "DECIMAL", "DECLARE", "DEFAULT",
			"DELAYED", "DELETE", "DENSE_RANK", "DESC", "DESCRIBE", "DETERMINISTIC", "DISTINCT",
			"DISTINCTROW", "DIV", "DOUBLE", "DROP", "DUAL", "EACH", "ELSE", "ELSEIF", "EMPTY", "ENCLOSED",
			"ESCAPED", "EXCEPT", "EXISTS", "EXIT", "EXPLAIN", "FALSE", "FETCH", "FIRST_VALUE", "FLOAT",
			"FLOAT4", "FLOAT8", "FOR", "FORCE", "FOREIGN", "FROM", "FULLTEXT", "FUNCTION", "GENERATED",
			"GET", "GRANT", "GROUP", "GROUPING", "GROUPS", "HAVING", "HIGH_PRIORITY", "HOUR_MICROSECOND",
			"HOUR_MINUTE", "HOUR_SECOND", "IF", "IGNORE", "IN", "INDEX", "INFILE", "INNER", "INOUT",
			"INSENSITIVE", "INSERT", "INT", "INT1", "INT2", "INT3", "INT4", "INT8", "INTEGER", "INTERSECT",
			"INTERVAL", "INTO", "IO_AFTER_GTIDS", "IO_BEFORE_GTIDS", "IS", "ITERATE", "JOIN", "JSON_TABLE",
			"KEY", "KEYS", "KILL", "LAG", "LAST_VALUE", "LATERAL", "LEAD", "LEADING", "LEAVE", "LEFT",
			"LIKE", "LIMIT", "LINEAR", "LINES", "LOAD", "LOCALTIME", "LOCALTIMESTAMP", "LOCK", "LONG",
			"LONGBLOB", "LONGTEXT", "LOOP", "LOW_PRIORITY", "MASTER_BIND", "MASTER_SSL_VERIFY_SERVER_CERT",
			"MATCH", "MAXVALUE", "MEDIUMBLOB", "MEDIUMINT", "MEDIUMTEXT", "MIDDLEINT", "MINUTE_MICROSECOND",
			"MINUTE_SECOND", "MOD", "MODIFIES", "NATURAL", "NOT", "NO_WRITE_TO_BINLOG", "NTH_VALUE",
			"NTILE", "NULL", "NUMERIC", "OF", "ON", "OPTIMIZE", "OPTIMIZER_COSTS", "OPTION", "OPTIONALLY",
			"OR", "ORDER", "OUT", "OUTER", "OUTFILE", "OVER", "PARTITION", "PERCENT_RANK", "PRECISION",
			"PRIMARY", "PROCEDURE", "PURGE", "RANGE", "RANK", "READ", "READS", "READ_WRITE", "REAL",
			"RECURSIVE", "REFERENCES", "REGEXP", "RELEASE", "RENAME", "REPEAT", "REPLACE", "REQUIRE",
			"RESIGNAL", "RESTRICT", "RETURN", "REVOKE", "RIGHT", "RLIKE", "ROW", "ROWS", "ROW_NUMBER",
			"SCHEMA", "SCHEMAS", "SECOND_MICROSECOND", "SELECT", "SENSITIVE", "SEPARATOR", "SET", "SHOW",
			"SIGNAL", "SMALLINT", "SPATIAL", "SPECIFIC", "SQL", "SQLEXCEPTION", "SQLSTATE", "SQLWARNING",
			"SQL_BIG_RESULT", "SQL_CALC_FOUND_ROWS", "SQL_SMALL_RESULT", "SSL", "STARTING", "STORED",
			"STRAIGHT_JOIN", "SYSTEM", "TABLE", "TERMINATED", "THEN", "TINYBLOB", "TINYINT", "TINYTEXT",
			"TO", "TRAILING", "TRIGGER", "TRUE", "UNDO", "UNION", "UNIQUE", "UNLOCK", "UNSIGNED", "UPDATE",
			"USAGE", "USE", "USING", "UTC_DATE", "UTC_TIME", "UTC_TIMESTAMP", "VALUES", "VARBINARY",
			"VARCHAR", "VARCHARACTER", "VARYING", "VIRTUAL", "WHEN", "WHERE", "WHILE", "WINDOW", "WITH",
			"WRITE", "XOR", "YEAR_MONTH", "ZEROFILL",
		),
		Postgres: wordSet(
			"ALL", "ANALYSE", "ANALYZE", "AND", "ANY", "ARRAY", "AS", "ASC", "ASYMMETRIC", "AUTHORIZATION",
			"BINARY", "BOTH", "CASE", "CAST", "CHECK", "COLLATE", "COLLATION", "COLUMN", "CONCURRENTLY",
			"CONSTRAINT", "CREATE", "CROSS", "CURRENT_CATALOG", "CURRENT_DATE", "CURRENT_ROLE",
			"CURRENT_SCHEMA", "CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_USER", "DEFAULT", "DEFERRABLE",
			"DESC", "DISTINCT", "DO", "ELSE", "END", "EXCEPT", "FALSE", "FETCH", "FOR", "FOREIGN", "FREEZE",
			"FROM", "FULL", "GRANT", "GROUP", "HAVING", "ILIKE", "IN", "INITIALLY", "INNER", "INTERSECT",
			"INTO", "IS", "ISNULL", "JOIN", "LATERAL", "LEADING", "LEFT", "LIKE", "LIMIT", "LOCALTIME",
			"LOCALTIMESTAMP", "NATURAL", "NOT", "NOTNULL", "NULL", "OFFSET", "ON", "ONLY", "OR", "ORDER",
			"OUTER", "OVERLAPS", "PLACING", "PRIMARY", "REFERENCES", "RETURNING", "RIGHT", "SELECT",
			"SESSION_USER", "SIMILAR", "SOME", "SYMMETRIC", "SYSTEM_USER", "TABLE", "TABLESAMPLE", "THEN",
			"TO", "TRAILING", "TRUE", "UNION", "UNIQUE", "USER", "USING", "VARIADIC", "VERBOSE", "WHEN",
			"WHERE", "WINDOW", "WITH",
		),
		// SQL Server's reserved keywords (Transact-SQL reference).
		SQLServer: wordSet(
			"ADD", "ALL", "ALTER", "AND", "ANY", "AS", "ASC", "AUTHORIZATION", "BACKUP", "BEGIN",
			"BETWEEN", "BREAK", "BROWSE", "BULK", "BY", "CASCADE", "CASE", "CHECK", "CHECKPOINT", "CLOSE",
			"CLUSTERED", "COALESCE", "COLLATE", "COLUMN", "COMMIT", "COMPUTE", "CONSTRAINT", "CONTAINS",
			"CONTAINSTABLE", "CONTINUE", "CONVERT", "CREATE", "CROSS", "CURRENT", "CURRENT_DATE",
			"CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_USER", "CURSOR", "DATABASE", "DBCC",
			"DEALLOCATE", "DECLARE", "DEFAULT", "DELETE", "DENY", "DESC", "DISK", "DISTINCT",
			"DISTRIBUTED", "DOUBLE", "DROP", "DUMP", "ELSE", "END", "ERRLVL", "ESCAPE", "EXCEPT", "EXEC",
			"EXECUTE", "EXISTS", "EXIT", "EXTERNAL", "FETCH", "FILE", "FILLFACTOR", "FOR", "FOREIGN",
			"FREETEXT", "FREETEXTTABLE", "FROM", "FULL", "FUNCTION", "GOTO", "GRANT", "GROUP", "HAVING",
			"HOLDLOCK", "IDENTITY", "IDENTITY_INSERT", "IDENTITYCOL", "IF", "IN", "INDEX", "INNER",
			"INSERT", "INTERSECT", "INTO", "IS", "JOIN", "KEY", "KILL", "LEFT", "LIKE", "LINENO", "LOAD",
			"MERGE", "NATIONAL", "NOCHECK", "NONCLUSTERED", "NOT", "NULL", "NULLIF", "OF", "OFF",
			"OFFSETS", "ON", "OPEN", "OPENDATASOURCE", "OPENQUERY", "OPENROWSET", "OPENXML", "OPTION",
			"OR", "ORDER", "OUTER", "OVER", "PERCENT", "PIVOT", "PLAN", "PRECISION", "PRIMARY", "PRINT",
			"PROC", "PROCEDURE", "PUBLIC", "RAISERROR", "READ", "READTEXT", "RECONFIGURE", "REFERENCES",
			"REPLICATION", "RESTORE", "RESTRICT", "RETURN", "REVERT", "REVOKE", "RIGHT", "ROLLBACK",
			"ROWCOUNT", "ROWGUIDCOL", "RULE", "SAVE", "SCHEMA", "SECURITYAUDIT", "SELECT",
			"SEMANTICKEYPHRASETABLE", "SEMANTICSIMILARITYDETAILSTABLE", "SEMANTICSIMILARITYTABLE",
			"SESSION_USER", "SET", "SETUSER", "SHUTDOWN", "SOME", "STATISTICS", "SYSTEM_USER", "TABLE",
			"TABLESAMPLE", "TEXTSIZE", "THEN", "TO", "TOP", "TRAN", "TRANSACTION", "TRIGGER", "TRUNCATE",
			"TRY_CONVERT", "TSEQUAL", "UNION", "UNIQUE", "UNPIVOT", "UPDATE", "UPDATETEXT", "USE", "USER",
			"VALUES", "VARYING", "VIEW", "WAITFOR", "WHEN", "WHERE", "WHILE", "WITH", "WITHIN",
			"WRITETEXT",
		),
		// Oracle's reserved words (V$RESERVED_WORDS with RESERVED = 'Y').
		Oracle: wordSet(
			"ACCESS", "ADD", "ALL", "ALTER", "AND", "ANY", "AS", "ASC", "AUDIT", "BETWEEN", "BY", "CHAR",
			"CHECK", "CLUSTER", "COLUMN", "COMMENT", "COMPRESS", "CONNECT", "CREATE", "CURRENT", "DATE",
			"DECIMAL", "DEFAULT", "DELETE", "DESC", "DISTINCT", "DROP", "ELSE", "EXCLUSIVE", "EXISTS",
			"FILE", "FLOAT", "FOR", "FROM", "GRANT", "GROUP", "HAVING", "IDENTIFIED", "IMMEDIATE", "IN",
			"INCREMENT", "INDEX", "INITIAL", "INSERT", "INTEGER", "INTERSECT", "INTO", "IS", "LEVEL", "LIKE",
			"LOCK", "LONG", "MAXEXTENTS", "MINUS", "MLSLABEL", "MODE", "MODIFY", "NOAUDIT", "NOCOMPRESS",
			"NOT", "NOWAIT", "NULL", "NUMBER", "OF", "OFFLINE", "ON", "ONLINE", "OPTION", "OR", "ORDER",
			"PCTFREE", "PRIOR", "PUBLIC", "RAW", "RENAME", "RESOURCE", "REVOKE", "ROW", "ROWID", "ROWNUM",
			"ROWS", "SELECT", "SESSION", "SET", "SHARE", "SIZE", "SMALLINT", "START", "SUCCESSFUL",
			"SYNONYM", "SYSDATE", "TABLE", "THEN", "TO", "TRIGGER", "UID", "UNION", "UNIQUE", "UPDATE",
			"USER", "VALIDATE", "VALUES", "VARCHAR", "VARCHAR2", "VIEW", "WHENEVER", "WHERE", "WITH",
		),
	}
)

// without returns list less the words drop.
func without(list []string, drop ...string) []string {
	return slices.DeleteFunc(list, func(w string) bool { return slices.Contains(drop, w) })
}

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
// a plain identifier, otherwise quoted as QuoteIdent does.
func QuoteIfNeeded(name string, d Dialect) string {
	if plainIdent(name, d) && !reserved[d][strings.ToUpper(name)] {
		return name
	}
	return QuoteIdent(d, name)
}

func plainIdent(s string, d Dialect) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case d == Oracle && (r == '_' || r == '#') && i > 0:
		case d == Oracle && r >= 'a' && r <= 'z':
			return false // unquoted names fold to upper case
		case r == '_' || r >= 'a' && r <= 'z':
			if d == Oracle {
				return false
			}
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
