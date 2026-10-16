package redit

import (
	"errors"
	"testing"
)

func TestCheckHolds(t *testing.T) {
	cases := []struct {
		c    Check
		v    any
		want bool
	}{
		{Check{Expect: ExpectValue, Value: "a"}, "a", true},
		{Check{Expect: ExpectValue, Value: "a"}, "b", false},
		{Check{Expect: ExpectValue, Value: "a"}, nil, false},
		{Check{Expect: ExpectValue, Value: "a"}, errors.New("WRONGTYPE"), false},
		{Check{Expect: ExpectNil}, nil, true},
		{Check{Expect: ExpectNil}, "x", false},
		{Check{Expect: ExpectNil}, errors.New("WRONGTYPE"), false},
		{Check{Expect: ExpectInt, Int: 0}, int64(0), true},
		{Check{Expect: ExpectInt, Int: 0}, int64(1), false},
		{Check{Expect: ExpectInt, Int: 0}, "0", false},
		{Check{Expect: ExpectScore, Score: 1.5}, "1.5", true},
		{Check{Expect: ExpectScore, Score: 1.5}, float64(1.5), true},
		{Check{Expect: ExpectScore, Score: 1.5}, "2", false},
		{Check{Expect: ExpectScore, Score: 1.5}, nil, false},
		{Check{Expect: ExpectLen, Int: 1}, []any{"x"}, true},
		{Check{Expect: ExpectLen, Int: 1}, []any{}, false},
		{Check{Expect: ExpectLen, Int: 0}, nil, false},
	}
	for i, c := range cases {
		if got := c.c.Holds(c.v); got != c.want {
			t.Errorf("%d: %+v.Holds(%#v) = %v, want %v", i, c.c, c.v, got, c.want)
		}
	}
}
