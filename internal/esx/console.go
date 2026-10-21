package esx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Request is one request of a REST console file.
type Request struct {
	Method string // upper case
	Path   string // without the leading "/"; may carry a query string
	// Body is what goes on the wire: the JSON as written with """ strings
	// turned into JSON strings; NDJSON with each line compacted and a
	// trailing newline; nil without a body.
	Body   []byte
	NDJSON bool // _bulk / _msearch
	// Line and End are the request line and the request's last line (0-based).
	Line, End int
}

// ParseError points at the console line a request failed to parse on.
type ParseError struct {
	Line int // 0-based
	Msg  string
}

func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line+1, e.Msg) }

var consoleMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "DELETE": true, "HEAD": true}

// requestLine parses "METHOD path".
func requestLine(line string) (method, path string, ok bool) {
	f := strings.Fields(line)
	if len(f) != 2 || !consoleMethods[strings.ToUpper(f[0])] {
		return "", "", false
	}
	return strings.ToUpper(f[0]), strings.TrimLeft(f[1], "/"), true
}

func isComment(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//")
}

// ParseConsole splits a console file into requests: a "METHOD path" line
// and the body lines up to the next request line. Lines starting with #
// or // are comments, outside """ strings.
func ParseConsole(text string) ([]Request, error) {
	lines := strings.Split(text, "\n")
	var reqs []Request
	var cur *Request
	var body []string
	var bodyLines []int
	inTriple := false
	finish := func() error {
		if cur == nil {
			return nil
		}
		if err := buildBody(cur, body, bodyLines); err != nil {
			return err
		}
		reqs = append(reqs, *cur)
		cur, body, bodyLines = nil, nil, nil
		return nil
	}
	for i, line := range lines {
		if !inTriple {
			if strings.TrimSpace(line) == "" || isComment(line) {
				continue
			}
			if m, p, ok := requestLine(line); ok {
				if err := finish(); err != nil {
					return nil, err
				}
				cur = &Request{Method: m, Path: p, Line: i, End: i}
				continue
			}
			if cur == nil {
				return nil, &ParseError{Line: i, Msg: "expected METHOD path"}
			}
		}
		if strings.Count(line, `"""`)%2 == 1 {
			inTriple = !inTriple
		}
		body = append(body, line)
		bodyLines = append(bodyLines, i)
		cur.End = i
	}
	if err := finish(); err != nil {
		return nil, err
	}
	return reqs, nil
}

// ndjsonAPIs take one JSON value per line.
var ndjsonAPIs = map[string]bool{"_bulk": true, "_msearch": true, "_msearch/template": true}

func isNDJSON(path string) bool {
	segs := splitPath(path)
	for i := range segs {
		if ndjsonAPIs[strings.Join(segs[i:], "/")] {
			return true
		}
	}
	return false
}

func buildBody(r *Request, body []string, lines []int) error {
	r.NDJSON = isNDJSON(r.Path)
	if len(body) == 0 {
		return nil
	}
	if r.NDJSON {
		var out bytes.Buffer
		for k, l := range body {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if err := json.Compact(&out, []byte(tripleQuotes(l))); err != nil {
				return &ParseError{Line: lines[k], Msg: "invalid JSON: " + err.Error()}
			}
			out.WriteByte('\n')
		}
		r.Body = out.Bytes()
		return nil
	}
	b := tripleQuotes(strings.Join(body, "\n"))
	var v any
	if err := json.Unmarshal([]byte(b), &v); err != nil {
		return &ParseError{Line: lines[0], Msg: "invalid JSON: " + err.Error()}
	}
	r.Body = []byte(b)
	return nil
}

// tripleQuotes turns Kibana's """raw strings""" into JSON strings.
func tripleQuotes(s string) string {
	var out strings.Builder
	for {
		start := strings.Index(s, `"""`)
		if start < 0 {
			break
		}
		end := strings.Index(s[start+3:], `"""`)
		if end < 0 {
			break
		}
		out.WriteString(s[:start])
		out.WriteString(jsonString(s[start+3 : start+3+end]))
		s = s[start+3+end+3:]
	}
	out.WriteString(s)
	return out.String()
}

func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// RequestAt is the request on line, or the one above when line is a
// blank or comment line after it.
func RequestAt(reqs []Request, line int) (Request, bool) {
	for i := len(reqs) - 1; i >= 0; i-- {
		if line >= reqs[i].Line {
			return reqs[i], true
		}
	}
	return Request{}, false
}
