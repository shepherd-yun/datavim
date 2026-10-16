package sqlx

import (
	"regexp"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

var (
	numericTypes = wordSet("INT", "INTEGER", "TINYINT", "SMALLINT", "MEDIUMINT", "BIGINT", "DECIMAL", "NUMERIC",
		"FLOAT", "DOUBLE", "REAL", "YEAR", "INT2", "INT4", "INT8", "FLOAT4", "FLOAT8", "OID",
		"MONEY", "SMALLMONEY", "NUMBER", "BINARY_FLOAT", "BINARY_DOUBLE", "BFLOAT", "BDOUBLE", "IBFLOAT", "IBDOUBLE")
	// oracleTypes write Oracle literals that do not depend on the session's
	// NLS formats; the values are as the driver shows them.
	oracleRaw  = wordSet("RAW", "LONGRAW", "OCIBLOBLOCATOR", "BLOB")
	oracleDate = map[string][2]string{
		"DATE":             {"TO_DATE", "YYYY-MM-DD HH24:MI:SS"},
		"TIMESTAMP":        {"TO_TIMESTAMP", "YYYY-MM-DD HH24:MI:SS.FF"},
		"TIMESTAMPDTY":     {"TO_TIMESTAMP", "YYYY-MM-DD HH24:MI:SS.FF"},
		"TIMESTAMPTZ":      {"TO_TIMESTAMP_TZ", "YYYY-MM-DD HH24:MI:SS.FF TZH:TZM"},
		"TIMESTAMPTZ_DTY":  {"TO_TIMESTAMP_TZ", "YYYY-MM-DD HH24:MI:SS.FF TZH:TZM"},
		"TIMESTAMPELTZ":    {"TO_TIMESTAMP_TZ", "YYYY-MM-DD HH24:MI:SS.FF TZH:TZM"},
		"TIMESTAMPLTZ_DTY": {"TO_TIMESTAMP_TZ", "YYYY-MM-DD HH24:MI:SS.FF TZH:TZM"},
	}
	binaryTypes = wordSet("BINARY", "VARBINARY", "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BIT", "GEOMETRY", "IMAGE")
	numberRe    = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][-+]?\d+)?$`)
)

// NormType upper-cases a driver type name and drops MySQL's UNSIGNED prefix.
func NormType(t string) string { return strings.TrimPrefix(strings.ToUpper(t), "UNSIGNED ") }

// IsNumericType reports a numeric column type, as drivers name them.
func IsNumericType(t string) bool { return numericTypes[NormType(t)] }

// IsBinaryType reports a MySQL or SQL Server binary column type.
func IsBinaryType(t string) bool { return binaryTypes[NormType(t)] }

// IsNumber reports text that is a plain decimal number.
func IsNumber(s string) bool { return numberRe.MatchString(s) }

// QuoteIdent quotes an identifier: `name` (MySQL), [name] (SQL Server) or
// "name" (PostgreSQL, Oracle).
func QuoteIdent(d Dialect, s string) string {
	//exhaustive:enforce
	switch d {
	case MySQL:
		return db.Quote(s, '`')
	case SQLServer:
		return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
	case Postgres, Oracle:
	}
	return db.Quote(s, '"')
}

// QualifiedName names a table in SQL text: database.table (MySQL),
// schema.table (PostgreSQL, Oracle) or database.schema.table (SQL Server),
// just the table without a namespace.
func QualifiedName(d Dialect, ref db.TableRef) string {
	name := QuoteIdent(d, ref.Name)
	if d == SQLServer && ref.Database != "" {
		// [db]..[t] is the table in the user's default schema of db
		schema := ""
		if ref.Schema != "" {
			schema = QuoteIdent(d, ref.Schema)
		}
		return QuoteIdent(d, ref.Database) + "." + schema + "." + name
	}
	ns := ref.Database
	if d.Features().Schemas {
		ns = ref.Schema
	}
	if ns == "" {
		return name
	}
	return QuoteIdent(d, ns) + "." + name
}

// Literal writes v as a SQL literal for a column of type dbType: NULL, a
// bare number for numeric columns, MySQL 0x… hex for binary columns, and
// otherwise a quoted string with ' doubled (and \ doubled on MySQL; N'…' on
// SQL Server).
func Literal(d Dialect, dbType string, v db.Value) string {
	switch {
	case v.Null:
		return "NULL"
	case IsNumericType(dbType) && IsNumber(v.Text):
		return v.Text
	case (d == MySQL || d == SQLServer) && IsBinaryType(dbType) && strings.HasPrefix(v.Text, "0x"):
		return v.Text
	case d == Oracle && oracleRaw[NormType(dbType)]:
		return "HEXTORAW('" + strings.ReplaceAll(v.Text, "'", "''") + "')"
	case d == Oracle && oracleDate[NormType(dbType)][0] != "":
		f := oracleDate[NormType(dbType)]
		return f[0] + "('" + strings.ReplaceAll(v.Text, "'", "''") + "', '" + f[1] + "')"
	}
	s := v.Text
	if d == MySQL {
		s = strings.ReplaceAll(s, `\`, `\\`)
	}
	s = "'" + strings.ReplaceAll(s, "'", "''") + "'"
	if d == SQLServer {
		return "N" + s // Unicode: without N, text outside the code page turns into ?
	}
	return s
}

// BeginSQL starts a transaction. Oracle has no BEGIN: SET TRANSACTION
// starts one explicitly.
func BeginSQL(d Dialect) string {
	//exhaustive:enforce
	switch d {
	case Postgres:
		return "BEGIN"
	case SQLServer:
		return "BEGIN TRANSACTION"
	case Oracle:
		return "SET TRANSACTION READ WRITE"
	case MySQL:
	}
	return "START TRANSACTION"
}
