package jsonmask

import (
	"strings"
	"testing"
)

func TestPaths(t *testing.T) {
	split := func(ps ...string) [][]string {
		var out [][]string
		for _, p := range ps {
			if p == "" {
				out = append(out, nil)
				continue
			}
			out = append(out, strings.Split(p, "."))
		}
		return out
	}
	for _, tc := range []struct {
		name, in, want string
		paths          [][]string
		changed        bool
	}{
		{"path", `{"id":1,"customer":{"phone":"555","name":"a"}}`, `{"id":1,"customer":{"phone":"******","name":"a"}}`, split("customer.phone"), true},
		{"array elements", `{"cards":[{"number":"4111","exp":"01/30"},{"number":"5500"}]}`, `{"cards":[{"number":"******","exp":"01/30"},{"number":"******"}]}`, split("cards[].number"), true},
		{"any key", `{"a":{"email":"x@y"},"b":{"email":"z@w","n":1},"c":2}`, `{"a":{"email":"******"},"b":{"email":"******","n":1},"c":2}`, split("*.email"), true},
		{"top-level array", `[{"ssn":"1"},{"ssn":"2"}]`, `[{"ssn":"******"},{"ssn":"******"}]`, split("[].ssn"), true},
		{"missing path", `{"id":1}`, `{"id":1}`, split("customer.phone"), false},
		{"numbers stay", `{"n":12345678901234567890,"secret":7,"f":1.50}`, `{"n":12345678901234567890,"secret":"******","f":1.50}`, split("secret"), true},
		{"object value", `{"a":{"b":1},"c":2}`, `{"a":"******","c":2}`, split("a"), true},
		{"empty path is the whole", `{"a":1}`, `"******"`, split(""), true},
		{"scalar", `7`, `7`, split("a"), false},
	} {
		got, changed := Paths([]byte(tc.in), tc.paths)
		if string(got) != tc.want || changed != tc.changed {
			t.Errorf("%s: got %s %v, want %s %v", tc.name, got, changed, tc.want, tc.changed)
		}
	}
	if Masked != "******" {
		t.Error("Masked")
	}
}
