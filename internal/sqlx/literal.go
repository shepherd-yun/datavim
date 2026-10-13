package sqlx

import (
	"regexp"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

var (
	numericTypes = wordSet("INT", "INTEGER", "TINYINT", "SMALLINT", "MEDIUMINT", "BIGINT", "DECIMAL", "NUMERIC",
		"FLOAT", "DOUBLE", "REAL", "YEAR", "INT2", "INT4", "INT8", "FLOAT4", "FLOAT8", "OID")
	binaryTypes = wordSet("BINARY", "VARBINARY", "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BIT", "GEOMETRY")
	numberRe    = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][-+]?\d+)?$`)
)

// NormType upper-cases a driver type name and drops MySQL's UNSIGNED prefix.
func NormType(t string) string { return strings.TrimPrefix(strings.ToUpper(t), "UNSIGNED ") }

// IsNumericType reports a numeric column type, as drivers name them.
func IsNumericType(t string) bool { return numericTypes[NormType(t)] }

// IsBinaryType reports a MySQL binary column type.
func IsBinaryType(t string) bool { return binaryTypes[NormType(t)] }

// IsNumber reports text that is a plain decimal number.
func IsNumber(s string) bool { return numberRe.MatchString(s) }

// QuoteIdent quotes an identifier: `name` (MySQL) or "name" (PostgreSQL).
func QuoteIdent(d Dialect, s string) string {
	if d == Postgres {
		return db.Quote(s, '"')
	}
	return db.Quote(s, '`')
}

// QualifiedName names a table in SQL text: database.table (MySQL) or
// schema.table (PostgreSQL), just the table without a namespace.
func QualifiedName(d Dialect, ref db.TableRef) string {
	ns := ref.Database
	if d == Postgres {
		ns = ref.Schema
	}
	if ns == "" {
		return QuoteIdent(d, ref.Name)
	}
	return QuoteIdent(d, ns) + "." + QuoteIdent(d, ref.Name)
}

// Literal writes v as a SQL literal for a column of type dbType: NULL, a
// bare number for numeric columns, MySQL 0x… hex for binary columns, and
// otherwise a quoted string with ' doubled (and \ doubled on MySQL).
func Literal(d Dialect, dbType string, v db.Value) string {
	switch {
	case v.Null:
		return "NULL"
	case IsNumericType(dbType) && IsNumber(v.Text):
		return v.Text
	case d == MySQL && IsBinaryType(dbType) && strings.HasPrefix(v.Text, "0x"):
		return v.Text
	}
	s := v.Text
	if d == MySQL {
		s = strings.ReplaceAll(s, `\`, `\\`)
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// BeginSQL starts a transaction.
func BeginSQL(d Dialect) string {
	if d == Postgres {
		return "BEGIN"
	}
	return "START TRANSACTION"
}
