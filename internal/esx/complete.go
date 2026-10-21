package esx

import (
	"regexp"
	"strings"
)

// CompleteKind is what the console completes at the cursor.
type CompleteKind int

const (
	CompleteNone CompleteKind = iota
	CompleteMethod
	CompletePath  // a path segment after the index ("_search")
	CompleteIndex // the path's first segment: an index or an API
	CompleteKey   // a body key from Keys
	CompleteField // a field name of Index's mapping
)

// CompleteCtx is the completion context at a cursor.
type CompleteCtx struct {
	Kind   CompleteKind
	Prefix string   // what is typed of the word so far
	Index  string   // the request path's index expression
	Keys   []string // CompleteKey's candidates
}

var (
	methodTyping = regexp.MustCompile(`^\s*[A-Za-z]*$`)
	pathTyping   = regexp.MustCompile(`^\s*([A-Za-z]+)\s+/?(\S*)$`)
)

// CompleteAt finds what to complete at line and col (0-based, col in
// runes) of a console file. The body is scanned leniently: it need not
// be complete JSON.
func CompleteAt(text string, line, col int) CompleteCtx {
	lines := strings.Split(text, "\n")
	if line < 0 || line >= len(lines) {
		return CompleteCtx{}
	}
	cur := []rune(lines[line])
	before := string(cur[:min(max(col, 0), len(cur))])
	if m := pathTyping.FindStringSubmatch(before); m != nil && consoleMethods[strings.ToUpper(m[1])] {
		p := m[2]
		if i := strings.LastIndex(p, "/"); i >= 0 {
			return CompleteCtx{Kind: CompletePath, Prefix: p[i+1:], Index: pathIndex(p)}
		}
		return CompleteCtx{Kind: CompleteIndex, Prefix: p}
	}
	req := -1
	for i := line - 1; i >= 0; i-- {
		if _, _, ok := requestLine(lines[i]); ok {
			req = i
			break
		}
	}
	var st jsonScan
	if req >= 0 {
		for _, l := range lines[req+1 : line] {
			st.line(l + "\n")
		}
		st.line(before)
	}
	if req < 0 || len(st.stack) == 0 && !st.inString {
		if methodTyping.MatchString(before) {
			return CompleteCtx{Kind: CompleteMethod, Prefix: strings.TrimSpace(before)}
		}
		return CompleteCtx{}
	}
	_, path, _ := requestLine(lines[req])
	ctx := st.context()
	ctx.Index = pathIndex(path)
	if ctx.Kind == CompleteNone {
		return CompleteCtx{}
	}
	return ctx
}

// pathIndex is a path's index expression: its first segment unless that
// is an API.
func pathIndex(path string) string {
	segs := splitPath(path)
	if len(segs) == 0 || strings.HasPrefix(segs[0], "_") && segs[0] != "_all" {
		return ""
	}
	return segs[0]
}

type scanFrame struct {
	obj        bool
	key        string // the key this container is the value of
	inArray    bool   // an object element of an array: key repeats the array's
	expectKey  bool
	pendingKey string
}

// jsonScan follows a partial JSON body: open containers, keys and
// whether the cursor is inside a string.
type jsonScan struct {
	stack    []scanFrame
	inString bool
	escaped  bool
	isKey    bool
	str      strings.Builder
}

func (s *jsonScan) top() *scanFrame {
	if len(s.stack) == 0 {
		return nil
	}
	return &s.stack[len(s.stack)-1]
}

// valueKey is the key a value opened now belongs to.
func (s *jsonScan) valueKey() (string, bool) {
	t := s.top()
	switch {
	case t == nil:
		return "", false
	case t.obj:
		return t.pendingKey, false
	}
	return t.key, true
}

func (s *jsonScan) line(l string) {
	if !s.inString && isComment(l) {
		return
	}
	for _, r := range l {
		if s.inString {
			switch {
			case s.escaped:
				s.escaped = false
				s.str.WriteRune(r)
			case r == '\\':
				s.escaped = true
			case r == '"':
				s.inString = false
				if t := s.top(); s.isKey && t != nil {
					t.pendingKey = s.str.String()
				}
			default:
				s.str.WriteRune(r)
			}
			continue
		}
		switch r {
		case '"':
			s.inString, s.str = true, strings.Builder{}
			t := s.top()
			s.isKey = t != nil && t.obj && t.expectKey
		case '{', '[':
			k, inArr := s.valueKey()
			s.stack = append(s.stack, scanFrame{obj: r == '{', key: k, inArray: inArr, expectKey: r == '{'})
		case '}', ']':
			if len(s.stack) > 0 {
				s.stack = s.stack[:len(s.stack)-1]
			}
		case ':':
			if t := s.top(); t != nil {
				t.expectKey = false
			}
		case ',':
			if t := s.top(); t != nil && t.obj {
				t.expectKey, t.pendingKey = true, ""
			}
		}
	}
}

func (s *jsonScan) context() CompleteCtx {
	t := s.top()
	if !s.inString || t == nil {
		return CompleteCtx{}
	}
	prefix := s.str.String()
	field := CompleteCtx{Kind: CompleteField, Prefix: prefix}
	if !s.isKey {
		if t.obj && fieldValueKeys[t.pendingKey] || !t.obj && fieldArrayKeys[t.key] {
			return field
		}
		return CompleteCtx{}
	}
	var chain []string
	for _, f := range s.stack[1:] {
		if !f.inArray {
			chain = append(chain, f.key)
		}
	}
	at := func(i int) string {
		if i < len(chain) {
			return chain[len(chain)-1-i]
		}
		return ""
	}
	keys := func(k []string) CompleteCtx { return CompleteCtx{Kind: CompleteKey, Prefix: prefix, Keys: k} }
	isAggs := func(k string) bool { return k == "aggs" || k == "aggregations" }
	switch last := at(0); {
	case len(chain) == 0:
		return keys(topKeys)
	case isAggs(at(1)):
		return keys(aggKeys)
	case isAggs(last):
		return CompleteCtx{} // naming an aggregation
	case isAggs(at(2)):
		return keys(aggParamKeys)
	case queryParents[last]:
		return keys(queryKeys)
	case last == "bool":
		return keys(boolKeys)
	case fieldQueries[last], last == "sort":
		return field
	case last == "highlight":
		return keys(highlightKeys)
	case last == "fields" && at(1) == "highlight":
		return field
	}
	return CompleteCtx{}
}
