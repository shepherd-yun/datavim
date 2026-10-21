package export

import "github.com/shepherd-yun/datavim/internal/sqlx"

var jsonTypes = map[string]bool{"JSON": true, "JSONB": true}

func isNumericType(t string) bool { return sqlx.IsNumericType(t) }
func isJSONType(t string) bool    { return jsonTypes[sqlx.NormType(t)] }
func isBoolType(t string) bool    { return sqlx.NormType(t) == "BOOL" }
func isNumber(s string) bool      { return sqlx.IsNumber(s) }
