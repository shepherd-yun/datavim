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

func TestSetPath(t *testing.T) {
	cases := []struct {
		src, path, v, want string
	}{
		{`{"z":1,"user":{"name":"a","age":3},"b":2}`, "user.name", `"x"`, `{"z":1,"user":{"name":"x","age":3},"b":2}`},
		{`{"a.b":1,"c":2}`, "a.b", `2`, `{"a.b":2,"c":2}`},
		{`{"a.b":1,"a":{"b":5}}`, "a.b", `2`, `{"a.b":2,"a":{"b":5}}`},
		{`{"c":1}`, "a.b.c", `true`, `{"c":1,"a":{"b":{"c":true}}}`},
		{`{"a":{"x":1}}`, "a.b", `null`, `{"a":{"x":1,"b":null}}`},
		{`{"a":{"x":1}}`, "a", `{"y":2}`, `{"a":{"y":2}}`},
		{`{"a":1.50,"b":"中"}`, "c", `3`, `{"a":1.50,"b":"中","c":3}`},
	}
	for _, tc := range cases {
		got, err := SetPath([]byte(tc.src), tc.path, []byte(tc.v))
		if err != nil || string(got) != tc.want {
			t.Errorf("SetPath(%s, %s) = %s, %v; want %s", tc.src, tc.path, got, err, tc.want)
		}
	}
	if _, err := SetPath([]byte(`{"a":[1]}`), "a.b", []byte("1")); err == nil {
		t.Error("set inside an array: no error")
	}
	if _, err := SetPath([]byte(`{"a":1}`), "a.b", []byte("1")); err == nil {
		t.Error("set below a scalar: no error")
	}
}
