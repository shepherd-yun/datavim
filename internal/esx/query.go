package esx

import (
	"encoding/json"
	"errors"
	"strings"
)

// SearchQuery turns the filter prompt's input into a query (spec §5.3):
// empty is match_all, a JSON object is used as is, anything else is a
// query_string with AND as the default operator.
func SearchQuery(input string) (json.RawMessage, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return json.RawMessage(`{"match_all":{}}`), nil
	}
	switch {
	case input[0] == '{' && json.Valid([]byte(input)):
		return json.RawMessage(input), nil
	case input[0] == '{', input[0] == '[' && json.Valid([]byte(input)):
		// a JSON array is no query; "[1 TO 5]" stays a query string range
		return nil, errors.New("a query DSL filter must be a JSON object")
	}
	return json.RawMessage(`{"query_string":{"query":` + jsonString(input) + `,"default_operator":"AND"}}`), nil
}

// qsSpecial are the characters query_string syntax gives a meaning to.
const qsSpecial = `+-=&|><!(){}[]^"~*?:\/`

// escapeTerm backslash-escapes the query_string specials and whitespace.
func escapeTerm(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(qsSpecial, r) || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// quote is a query_string phrase: only " and \ need escaping inside.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// FieldEquals is `field:"value"`, matching value exactly whatever it holds.
func FieldEquals(field, value string) string { return escapeTerm(field) + ":" + quote(value) }

// FieldNot is `NOT field:"value"`.
func FieldNot(field, value string) string { return "NOT " + FieldEquals(field, value) }

// FieldContains is `field:*value*`, value's wildcards escaped.
func FieldContains(field, value string) string {
	return escapeTerm(field) + ":*" + escapeTerm(value) + "*"
}

// FieldCompare is `field:>value` or `field:<value`.
func FieldCompare(field, op, value string) string {
	return escapeTerm(field) + ":" + op + escapeTerm(value)
}

// FieldExists is `_exists_:field`, or `NOT _exists_:field`.
func FieldExists(field string, exists bool) string {
	if exists {
		return "_exists_:" + escapeTerm(field)
	}
	return "NOT _exists_:" + escapeTerm(field)
}

// AndQuery appends clause to the current filter with AND; cur gets
// parentheses unless it is a single clause.
func AndQuery(cur, clause string) string {
	cur = strings.TrimSpace(cur)
	switch {
	case cur == "":
		return clause
	case singleClause(cur):
		return cur + " AND " + clause
	}
	return "(" + cur + ") AND " + clause
}

// singleClause reports a query with no whitespace outside quotes,
// brackets and escapes.
func singleClause(q string) bool {
	depth, inQuote, esc := 0, false, false
	for _, r := range q {
		switch {
		case esc:
			esc = false
		case r == '\\':
			esc = true
		case r == '"':
			inQuote = !inQuote
		case inQuote:
		case r == '[' || r == '{' || r == '(':
			depth++
		case r == ']' || r == '}' || r == ')':
			depth--
		case depth == 0 && (r == ' ' || r == '\t'):
			return false
		}
	}
	return true
}
