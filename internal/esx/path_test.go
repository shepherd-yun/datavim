package esx

import "testing"

func TestGetPath(t *testing.T) {
	cases := []struct {
		src, path, want string
		ok              bool
	}{
		{`{"user":{"name":"张三"}}`, "user.name", `"张三"`, true},
		{`{"user.name":"x"}`, "user.name", `"x"`, true},
		{`{"a.b":1,"a":{"b":2}}`, "a.b", `1`, true},
		{`{"a":{"b.c":3}}`, "a.b.c", `3`, true},
		{`{"a":[1,2]}`, "a.0", ``, false},
		{`{"a":[1,2]}`, "a", `[1,2]`, true},
		{`{"a":[{"b":1},{"b":2}]}`, "a.b", `[1,2]`, true},
		{`{"a":null}`, "a", `null`, true},
		{`{"a":1}`, "b", ``, false},
		{`{"a":1}`, "a.b", ``, false},
	}
	for _, tc := range cases {
		v, ok := GetPath([]byte(tc.src), tc.path)
		if ok != tc.ok || string(v) != tc.want {
			t.Errorf("%s %s: %s %v", tc.src, tc.path, v, ok)
		}
	}
}

func TestCellText(t *testing.T) {
	cases := []struct {
		v    string
		ok   bool
		text string
		null bool
	}{
		{``, false, "", false},
		{`null`, true, "", true},
		{`"a\"b"`, true, `a"b`, false},
		{`12.50`, true, "12.50", false},
		{`{ "a" : 1 }`, true, `{"a":1}`, false},
	}
	for _, tc := range cases {
		text, null := CellText([]byte(tc.v), tc.ok)
		if text != tc.text || null != tc.null {
			t.Errorf("%q: %q %v", tc.v, text, null)
		}
	}
}
