package export

import (
	"regexp"
	"strings"
)

func set(ws ...string) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[w] = true
	}
	return m
}

var (
	numericTypes = set("INT", "INTEGER", "TINYINT", "SMALLINT", "MEDIUMINT", "BIGINT", "DECIMAL", "NUMERIC",
		"FLOAT", "DOUBLE", "REAL", "YEAR", "INT2", "INT4", "INT8", "FLOAT4", "FLOAT8", "OID")
	binaryTypes = set("BINARY", "VARBINARY", "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BIT", "GEOMETRY")
	jsonTypes   = set("JSON", "JSONB")
	numberRe    = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][-+]?\d+)?$`)
)

func normType(t string) string { return strings.TrimPrefix(strings.ToUpper(t), "UNSIGNED ") }

func isNumericType(t string) bool { return numericTypes[normType(t)] }
func isBinaryType(t string) bool  { return binaryTypes[normType(t)] }
func isJSONType(t string) bool    { return jsonTypes[normType(t)] }
func isBoolType(t string) bool    { return normType(t) == "BOOL" }
func isNumber(s string) bool      { return numberRe.MatchString(s) }
