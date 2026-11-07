package sqlite

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// bindArgs writes args into the ? placeholders of stmt as literals. Both
// engines send text: the sqlite3 shell has no parameters worth using.
func bindArgs(stmt string, args []any) (string, error) {
	at := sqlx.Placeholders(stmt, sqlx.SQLite)
	if len(at) != len(args) {
		return "", fmt.Errorf("statement has %d placeholders, got %d arguments", len(at), len(args))
	}
	var b strings.Builder
	last := 0
	for i, pos := range at {
		lit, err := literal(args[i])
		if err != nil {
			return "", err
		}
		b.WriteString(stmt[last:pos])
		b.WriteString(lit)
		last = pos + 1
	}
	b.WriteString(stmt[last:])
	return b.String(), nil
}

func literal(v any) (string, error) {
	switch v := v.(type) {
	case nil:
		return "NULL", nil
	case bool:
		if v {
			return "1", nil
		}
		return "0", nil
	case int:
		return strconv.Itoa(v), nil
	case int32:
		return strconv.FormatInt(int64(v), 10), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case uint64:
		return strconv.FormatUint(v, 10), nil
	case float64:
		return realText(v), nil
	case string:
		return stringLiteral(v), nil
	case []byte:
		return blobText(v), nil
	}
	return "", fmt.Errorf("cannot bind a %T", v)
}

// stringLiteral quotes s. SQL text ends at a NUL, so NUL bytes are spliced in.
func stringLiteral(s string) string {
	parts := strings.Split(s, "\x00")
	for i, p := range parts {
		parts[i] = "'" + strings.ReplaceAll(p, "'", "''") + "'"
	}
	return strings.Join(parts, "||char(0)||")
}

// blobText is how a BLOB cell reads and is written back, on both engines.
func blobText(b []byte) string { return "x'" + strings.ToUpper(hex.EncodeToString(b)) + "'" }

// realInf is an infinite REAL as SQL can write it.
const realInf = "9.0e+999"

// realText writes a REAL the way the sqlite3 shell does: with a decimal point
// or an exponent, so it stays a REAL when written back.
func realText(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return realInf
	case math.IsInf(f, -1):
		return "-" + realInf
	case math.IsNaN(f):
		return "NULL"
	}
	if a := math.Abs(f); a != 0 && (a < 1e-4 || a >= 1e15) {
		s := strconv.FormatFloat(f, 'e', -1, 64)
		if m, e, ok := strings.Cut(s, "e"); ok && !strings.Contains(m, ".") {
			return m + ".0e" + e
		}
		return s
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}
