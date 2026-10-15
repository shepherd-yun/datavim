package mysql

import (
	"regexp"
	"strings"
)

var (
	intDisplayWidth = regexp.MustCompile(`^(tinyint|smallint|mediumint|int|bigint)\(\d+\)`)
	numericColumn   = regexp.MustCompile(`^(tinyint|smallint|mediumint|int|bigint|decimal|numeric|float|double|real|year)\b`)
	plainNumber     = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][-+]?\d+)?$`)
	currentTime     = regexp.MustCompile(`(?i)^(current_timestamp|now|localtime|localtimestamp)(\(\d*\))?$`)
)

// normType lower-cases a column type and drops integer display widths,
// which MySQL 8 ignores, except tinyint(1), the conventional boolean.
func normType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if strings.HasPrefix(t, "tinyint(1)") {
		return t
	}
	return intDisplayWidth.ReplaceAllString(t, "$1")
}

// normDefault turns COLUMN_DEFAULT, which shows strings without quotes,
// into SQL text: expressions (extra, the lower-case EXTRA, says
// default_generated) as MySQL keeps them, in parentheses unless they are
// CURRENT_TIMESTAMP and the like; numbers and bit values bare; everything
// else quoted.
func normDefault(def *string, typ, extra string) *string {
	if def == nil {
		return nil
	}
	d := *def
	switch {
	case strings.Contains(extra, "default_generated"):
		if !currentTime.MatchString(d) {
			d = "(" + d + ")"
		}
	case numericColumn.MatchString(typ) && plainNumber.MatchString(d):
	case strings.HasPrefix(typ, "bit") && (strings.HasPrefix(d, "b'") || strings.HasPrefix(d, "0x")):
	default:
		d = quoteString(d)
	}
	return &d
}

// onUpdate is the ON UPDATE expression in extra, the lower-case EXTRA of
// a column ("default_generated on update current_timestamp(3)"), or "".
func onUpdate(extra string) string {
	_, after, ok := strings.Cut(extra, "on update ")
	if !ok {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(after))
}

// quoteString writes s as a string literal. The server reads backslash
// escapes in literals, so backslashes are doubled as well as quotes.
func quoteString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s) + "'"
}
