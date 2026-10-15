package postgres

import (
	"regexp"
	"strings"
)

var (
	timeZoneType = regexp.MustCompile(`^(timestamp|time)(\(\d+\))? with(out)? time zone(\[\])?$`)
	castLiteral  = regexp.MustCompile(`^('(?:[^']|'')*'|-?\d+(?:\.\d+)?)::[a-z][a-z0-9_ ]*(?:\(\d+(?:,\s*\d+)?\))?(?:\[\])?$`)
	typeAliases  = []struct{ long, short string }{
		{"character varying", "varchar"},
		{"character", "char"},
		{"integer", "int"},
		{"boolean", "bool"},
	}
)

// normType writes format_type's long names the short way people type them:
// varchar, char, int, bool, timestamp, timestamptz.
func normType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if m := timeZoneType.FindStringSubmatch(t); m != nil {
		base := m[1]
		if m[3] == "" { // "with time zone"
			base += "tz"
		}
		return base + m[2] + m[4]
	}
	for _, a := range typeAliases {
		if rest, ok := strings.CutPrefix(t, a.long); ok && (rest == "" || rest[0] == '(' || rest[0] == '[') {
			return a.short + rest
		}
	}
	return t
}

// normDefault drops the cast PostgreSQL puts after literal defaults
// ('x'::character varying → 'x') and reads an explicit NULL default as none.
func normDefault(d *string) *string {
	if d == nil {
		return nil
	}
	s := *d
	if m := castLiteral.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	if l := strings.ToLower(s); l == "null" || strings.HasPrefix(l, "null::") {
		return nil
	}
	return &s
}

// quoteString writes s as a string literal (standard_conforming_strings is on).
func quoteString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
