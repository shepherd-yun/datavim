package esx

import (
	"encoding/json"
	"testing"
)

func TestSearchQuery(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"", `{"match_all":{}}`, false},
		{"  ", `{"match_all":{}}`, false},
		{"88121", `{"query_string":{"query":"88121","default_operator":"AND"}}`, false},
		{"status:paid AND x", `{"query_string":{"query":"status:paid AND x","default_operator":"AND"}}`, false},
		{`{"term":{"a":1}}`, `{"term":{"a":1}}`, false},
		{"[1 TO 5]", `{"query_string":{"query":"[1 TO 5]","default_operator":"AND"}}`, false},
		{"{bad", "", true},
		{"[1]", "", true},
	}
	for _, tc := range cases {
		got, err := SearchQuery(tc.in)
		if (err != nil) != tc.err || !tc.err && string(got) != tc.want {
			t.Errorf("%q: %s %v", tc.in, got, err)
		}
	}
}

func TestFieldEquals(t *testing.T) {
	cases := []struct{ field, value, want string }{
		{"url", "http://a/b?c=1", `url:"http://a/b?c=1"`},
		{"a", "a+b", `a:"a+b"`},
		{"name", "中文 空格", `name:"中文 空格"`},
		{"msg", `say "hi"`, `msg:"say \"hi\""`},
		{"p", `C:\path`, `p:"C:\\path"`},
		{"user.name", "x", `user.name:"x"`},
		{"my field", "x", `my\ field:"x"`},
		{"a:b", "x", `a\:b:"x"`},
	}
	for _, tc := range cases {
		got := FieldEquals(tc.field, tc.value)
		if got != tc.want {
			t.Errorf("FieldEquals(%q, %q) = %s, want %s", tc.field, tc.value, got, tc.want)
		}
		q, err := SearchQuery(got)
		if err != nil || !json.Valid(q) {
			t.Errorf("%s: %s %v", got, q, err)
		}
		var back struct {
			QS struct{ Query string } `json:"query_string"`
		}
		if json.Unmarshal(q, &back) != nil || back.QS.Query != got {
			t.Errorf("%s did not round-trip: %s", got, q)
		}
	}
	if got := FieldNot("a", "x"); got != `NOT a:"x"` {
		t.Errorf("FieldNot = %s", got)
	}
}

func TestFieldContains(t *testing.T) {
	cases := []struct{ field, value, want string }{
		{"a", "abc", `a:*abc*`},
		{"a", "a*b?c", `a:*a\*b\?c*`},
		{"a", "x y", `a:*x\ y*`},
		{"a", `p/q:r`, `a:*p\/q\:r*`},
	}
	for _, tc := range cases {
		if got := FieldContains(tc.field, tc.value); got != tc.want {
			t.Errorf("FieldContains(%q) = %s, want %s", tc.value, got, tc.want)
		}
	}
	if got := FieldCompare("amount", ">", "100"); got != `amount:>100` {
		t.Errorf("FieldCompare = %s", got)
	}
	if got := FieldCompare("d", "<", "2026-10-20"); got != `d:<2026\-10\-20` {
		t.Errorf("FieldCompare = %s", got)
	}
}

func TestFieldExists(t *testing.T) {
	if got := FieldExists("a.b", true); got != "_exists_:a.b" {
		t.Errorf("exists = %s", got)
	}
	if got := FieldExists("a.b", false); got != "NOT _exists_:a.b" {
		t.Errorf("missing = %s", got)
	}
}

func TestAndQuery(t *testing.T) {
	cases := []struct{ cur, clause, want string }{
		{"", `a:"x"`, `a:"x"`},
		{"  ", `a:"x"`, `a:"x"`},
		{`a:"x y"`, `b:"z"`, `a:"x y" AND b:"z"`},
		{`a:x\ y`, `b:"z"`, `a:x\ y AND b:"z"`},
		{`status:paid AND 88121`, `b:"z"`, `(status:paid AND 88121) AND b:"z"`},
		{`NOT a:"x"`, `b:"z"`, `(NOT a:"x") AND b:"z"`},
		{`a:[1 TO 5]`, `b:"z"`, `a:[1 TO 5] AND b:"z"`},
	}
	for _, tc := range cases {
		if got := AndQuery(tc.cur, tc.clause); got != tc.want {
			t.Errorf("AndQuery(%q) = %s, want %s", tc.cur, got, tc.want)
		}
	}
}
