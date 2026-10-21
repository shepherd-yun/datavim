package kmsgx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/shepherd-yun/datavim/internal/kafka"
)

// Filter is a parsed message filter. The zero Filter matches everything.
type Filter struct {
	src  string
	root node
}

// node is one part of a filter expression.
type node interface{ match(r kafka.Record) bool }

type (
	orNode  []node
	andNode []node
)

func (n orNode) match(r kafka.Record) bool {
	for _, c := range n {
		if c.match(r) {
			return true
		}
	}
	return false
}

func (n andNode) match(r kafka.Record) bool {
	for _, c := range n {
		if !c.match(r) {
			return false
		}
	}
	return true
}

// cond is one field op value comparison.
type cond struct {
	field string // key, value, partition, offset, ts, size, header.<name>, value.<path>
	op    string
	value string
	num   float64
	isNum bool
	time  time.Time
}

// ParseFilter reads a filter expression: field op value, joined with and /
// or (and binds tighter; parentheses group). Fields are key, value,
// header.<name>, partition, offset, ts, size and value.<JSON path> (dot
// separated, array indexes as numbers: items.0.sku). Operators are = != ~
// (contains, ignoring case) > < >= <=. A value may be in double quotes. ts
// takes a time as in ParsePosition: 09:12, 2026-10-20 09:12 or -15m.
func ParseFilter(s string, now time.Time) (Filter, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Filter{}, nil
	}
	toks, err := lexFilter(s)
	if err != nil {
		return Filter{}, err
	}
	p := &filterParser{toks: toks, end: len(s) + 1, now: now}
	root, err := p.or()
	if err != nil {
		return Filter{}, err
	}
	if t, ok := p.peek(); ok {
		return Filter{}, fmt.Errorf("column %d: expected and / or, got %q", t.col, t.text)
	}
	return Filter{src: s, root: root}, nil
}

// Match reports whether r passes the filter.
func (f Filter) Match(r kafka.Record) bool { return f.root == nil || f.root.match(r) }

// String is the expression the filter was parsed from.
func (f Filter) String() string { return f.src }

// IsZero reports whether f matches everything.
func (f Filter) IsZero() bool { return f.root == nil }

type token struct {
	text   string
	col    int  // 1-based
	quoted bool // a "…" value
	op     bool
}

func lexFilter(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c == '(' || c == ')':
			out = append(out, token{text: s[i : i+1], col: i + 1})
			i++
		case c == '"':
			var b strings.Builder
			j := i + 1
			for ; j < len(s) && s[j] != '"'; j++ {
				if s[j] == '\\' && j+1 < len(s) {
					j++
				}
				b.WriteByte(s[j])
			}
			if j >= len(s) {
				return nil, fmt.Errorf("column %d: unterminated quote", i+1)
			}
			out = append(out, token{text: b.String(), col: i + 1, quoted: true})
			i = j + 1
		case strings.ContainsRune("=!~<>", rune(c)):
			j := i + 1
			if j < len(s) && s[j] == '=' && c != '=' && c != '~' {
				j++
			}
			if c == '!' && j == i+1 {
				return nil, fmt.Errorf("column %d: did you mean != ?", i+1)
			}
			out = append(out, token{text: s[i:j], col: i + 1, op: true})
			i = j
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t()\"=!~<>", rune(s[j])) {
				j++
			}
			// a date then a time is one value: ts > 2026-10-20 09:12
			if isDate(s[i:j]) {
				k := j
				for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
					k++
				}
				e := k
				for e < len(s) && !strings.ContainsRune(" \t()\"=!~<>", rune(s[e])) {
					e++
				}
				if e > k && isClock(s[k:e]) {
					out = append(out, token{text: s[i:j] + " " + s[k:e], col: i + 1})
					i = e
					continue
				}
			}
			out = append(out, token{text: s[i:j], col: i + 1})
			i = j
		}
	}
	return out, nil
}

func isDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func isClock(s string) bool {
	for _, layout := range []string{"15:04", "15:04:05"} {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}
	return false
}

type filterParser struct {
	toks []token
	pos  int
	end  int // the column after the input
	now  time.Time
}

func (p *filterParser) peek() (token, bool) {
	if p.pos < len(p.toks) {
		return p.toks[p.pos], true
	}
	return token{}, false
}

func (p *filterParser) col() int {
	if t, ok := p.peek(); ok {
		return t.col
	}
	return p.end
}

func (p *filterParser) keyword(w string) bool {
	t, ok := p.peek()
	if ok && !t.quoted && !t.op && strings.EqualFold(t.text, w) {
		p.pos++
		return true
	}
	return false
}

func (p *filterParser) or() (node, error) {
	var n orNode
	for {
		a, err := p.and()
		if err != nil {
			return nil, err
		}
		n = append(n, a)
		if !p.keyword("or") {
			break
		}
	}
	if len(n) == 1 {
		return n[0], nil
	}
	return n, nil
}

func (p *filterParser) and() (node, error) {
	var n andNode
	for {
		c, err := p.term()
		if err != nil {
			return nil, err
		}
		n = append(n, c)
		if !p.keyword("and") {
			break
		}
	}
	if len(n) == 1 {
		return n[0], nil
	}
	return n, nil
}

func (p *filterParser) term() (node, error) {
	t, ok := p.peek()
	if ok && t.text == "(" && !t.quoted {
		p.pos++
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		if t, ok := p.peek(); !ok || t.text != ")" || t.quoted {
			return nil, fmt.Errorf("column %d: expected )", p.col())
		}
		p.pos++
		return n, nil
	}
	if !ok || t.op || t.quoted || t.text == ")" {
		return nil, fmt.Errorf("column %d: expected a field (key, value, header.x, partition, offset, ts, size, value.path)", p.col())
	}
	field := t.text
	switch {
	case field == "key", field == "value", field == "partition", field == "offset", field == "ts", field == "size":
	case strings.HasPrefix(field, "header.") && len(field) > len("header."):
	case strings.HasPrefix(field, "value.") && len(field) > len("value."):
	default:
		return nil, fmt.Errorf("column %d: unknown field %s", t.col, field)
	}
	p.pos++
	op, ok := p.peek()
	if !ok || !op.op {
		return nil, fmt.Errorf("column %d: expected an operator (= != ~ > <)", p.col())
	}
	p.pos++
	v, ok := p.peek()
	if !ok || v.op || (!v.quoted && (v.text == "(" || v.text == ")")) {
		return nil, fmt.Errorf("column %d: expected a value after %s", p.col(), op.text)
	}
	p.pos++
	c := &cond{field: field, op: op.text, value: v.text}
	c.num, c.isNum = number(v.text)
	switch field {
	case "partition", "offset", "size":
		if !c.isNum {
			return nil, fmt.Errorf("column %d: %s needs a number, got %q", v.col, field, v.text)
		}
	case "ts":
		tm, err := parseTime(v.text, p.now)
		if err != nil {
			return nil, fmt.Errorf("column %d: %w", v.col, err)
		}
		c.time = tm
	}
	return c, nil
}

func number(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f, err == nil
}

func (c *cond) match(r kafka.Record) bool {
	switch {
	case c.field == "key":
		return !r.KeyNull && c.compare(string(r.Key))
	case c.field == "value":
		return !r.ValueNull && c.compare(string(r.Value))
	case c.field == "partition":
		return c.compareNum(float64(r.Partition))
	case c.field == "offset":
		return c.compareNum(float64(r.Offset))
	case c.field == "size":
		return c.compareNum(float64(r.Size))
	case c.field == "ts":
		return c.compareTime(r.Time)
	case strings.HasPrefix(c.field, "header."):
		name := c.field[len("header."):]
		for _, h := range r.Headers {
			if h.Key == name && c.compare(string(h.Value)) {
				return true
			}
		}
		return false
	default: // value.<path>
		v, ok := jsonPath(r.Value, c.field[len("value."):])
		return ok && c.compare(v)
	}
}

// compare compares a field's text: as numbers when both sides are numbers.
func (c *cond) compare(s string) bool {
	if c.op == "~" {
		return strings.Contains(strings.ToLower(s), strings.ToLower(c.value))
	}
	if n, ok := number(s); ok && c.isNum {
		return c.compareNum(n)
	}
	return cmpOp(c.op, strings.Compare(s, c.value))
}

func (c *cond) compareNum(n float64) bool {
	if c.op == "~" {
		return strings.Contains(strconv.FormatFloat(n, 'f', -1, 64), c.value)
	}
	switch {
	case n < c.num:
		return cmpOp(c.op, -1)
	case n > c.num:
		return cmpOp(c.op, 1)
	}
	return cmpOp(c.op, 0)
}

func (c *cond) compareTime(t time.Time) bool {
	if c.op == "=" || c.op == "!=" || c.op == "~" {
		eq := t.Truncate(time.Second).Equal(c.time)
		return eq == (c.op != "!=")
	}
	return cmpOp(c.op, t.Compare(c.time))
}

func cmpOp(op string, cmp int) bool {
	switch op {
	case "=":
		return cmp == 0
	case "!=":
		return cmp != 0
	case ">":
		return cmp > 0
	case "<":
		return cmp < 0
	case ">=":
		return cmp >= 0
	case "<=":
		return cmp <= 0
	}
	return false
}

// jsonPath looks path up in a JSON object value: a string leaf is its
// text, anything else its compact JSON.
func jsonPath(value []byte, path string) (string, bool) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return "", false
	}
	for part := range strings.SplitSeq(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			if v, ok = x[part]; !ok {
				return "", false
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return "", false
			}
			v = x[i]
		default:
			return "", false
		}
	}
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	}
	out, err := json.Marshal(v)
	return string(out), err == nil
}

// CellFilter is the condition ff / f! / fs add for a table cell: column is
// the column name (KEY, VALUE, PART / partition, OFFSET), op "=", "!=" or
// "~". It is joined to a non-empty existing expression with " and ",
// in parentheses when that expression is an or.
func CellFilter(existing, column, op, value string) string {
	field := strings.ToLower(column)
	if field == "part" {
		field = "partition"
	}
	c := field + " " + op + " " + quoteValue(value)
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return c
	}
	if f, err := ParseFilter(existing, time.Now()); err == nil {
		if _, ok := f.root.(orNode); ok {
			existing = "(" + existing + ")"
		}
	}
	return existing + " and " + c
}

// quoteValue quotes v when it would not read back as one bare value.
func quoteValue(v string) string {
	bare := v != "" && !strings.EqualFold(v, "and") && !strings.EqualFold(v, "or") &&
		strings.IndexFunc(v, func(r rune) bool {
			return unicode.IsSpace(r) || strings.ContainsRune("()\"=!~<>\\", r)
		}) < 0
	if bare {
		return v
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}
