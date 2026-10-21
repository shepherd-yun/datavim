package esx

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func compact(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatalf("compact %q: %v", s, err)
	}
	return b.String()
}

func TestParseConsole(t *testing.T) {
	text := `# orders
GET orders/_search
{
  "query": { "term": { "order_no": 88121 } }
}

// cat
get /_cat/indices?v
`
	reqs, err := ParseConsole(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("got %d requests: %+v", len(reqs), reqs)
	}
	r := reqs[0]
	if r.Method != "GET" || r.Path != "orders/_search" || r.Line != 1 || r.End != 4 || r.NDJSON {
		t.Errorf("first = %+v", r)
	}
	if got := compact(t, string(r.Body)); got != `{"query":{"term":{"order_no":88121}}}` {
		t.Errorf("body = %s", got)
	}
	r = reqs[1]
	if r.Method != "GET" || r.Path != "_cat/indices?v" || r.Line != 7 || r.End != 7 || r.Body != nil {
		t.Errorf("second = %+v", r)
	}
}

func TestParseConsoleNDJSON(t *testing.T) {
	reqs, err := ParseConsole("POST _bulk\n{\"index\":{\"_index\":\"t\",\"_id\":\"1\"}}\n{ \"a\": 1 }\n\n")
	if err != nil {
		t.Fatal(err)
	}
	r := reqs[0]
	if !r.NDJSON || string(r.Body) != "{\"index\":{\"_index\":\"t\",\"_id\":\"1\"}}\n{\"a\":1}\n" || r.End != 2 {
		t.Errorf("bulk = %+v body %q", r, r.Body)
	}
}

func TestParseConsoleTripleQuotes(t *testing.T) {
	text := "POST t/_update/1\n{\"script\":{\"source\":\"\"\"ctx._source.a = \"x\"\n// keep me\n\"\"\"}}\n"
	reqs, err := ParseConsole(text)
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Script struct{ Source string } `json:"script"`
	}
	if err := json.Unmarshal(reqs[0].Body, &b); err != nil {
		t.Fatalf("body %s: %v", reqs[0].Body, err)
	}
	if b.Script.Source != "ctx._source.a = \"x\"\n// keep me\n" {
		t.Errorf("source = %q", b.Script.Source)
	}
}

func TestParseConsoleErrors(t *testing.T) {
	cases := []struct{ text, want string }{
		{"GET a\n\nPOST b/_search\n{\"query\": \n", "line 4: invalid JSON"},
		{"orders/_search\n", "line 1: expected METHOD path"},
		{"PATCH x\n", "line 1: expected METHOD path"},
		{"GET\n", "line 1: expected METHOD path"},
		{"POST _bulk\n{\"index\":{}}\n{bad\n", "line 3: invalid JSON"},
	}
	for _, tc := range cases {
		_, err := ParseConsole(tc.text)
		var pe *ParseError
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want %q", tc.text, err, tc.want)
		} else if !asParseError(err, &pe) {
			t.Errorf("%q: err is %T", tc.text, err)
		}
	}
}

func asParseError(err error, pe **ParseError) bool {
	p, ok := err.(*ParseError)
	*pe = p
	return ok
}

func TestRequestAt(t *testing.T) {
	text := "# top\nGET a/_search\n{\n  \"size\": 1\n}\n\nGET b\n"
	reqs, err := ParseConsole(text)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		line int
		path string
		ok   bool
	}{
		{0, "", false},
		{3, "a/_search", true},
		{5, "a/_search", true},
		{6, "b", true},
		{7, "b", true},
	}
	for _, tc := range cases {
		r, ok := RequestAt(reqs, tc.line)
		if ok != tc.ok || r.Path != tc.path {
			t.Errorf("line %d: %q %v", tc.line, r.Path, ok)
		}
	}
}
