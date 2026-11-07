package sqlite

import (
	"math"
	"testing"
)

func TestBindArgs(t *testing.T) {
	tests := []struct {
		stmt string
		args []any
		want string
	}{
		{"select ?", []any{nil}, "select NULL"},
		{"select ?", []any{int64(5)}, "select 5"},
		{"select ?", []any{7}, "select 7"},
		{"select ?", []any{1.5}, "select 1.5"},
		{"select ?", []any{2.0}, "select 2.0"},
		{"select ?", []any{math.Inf(1)}, "select 9.0e+999"},
		{"select ?", []any{"it's"}, "select 'it''s'"},
		{"select ?", []any{"a\x00b"}, "select 'a'||char(0)||'b'"},
		{"select ?", []any{[]byte{0, 0xff}}, "select x'00FF'"},
		{"select ?, ?", []any{true, false}, "select 1, 0"},
		{"select '?', /* ? */ ?, \"?\" -- ?", []any{1}, "select '?', /* ? */ 1, \"?\" -- ?"},
		{"select 1", nil, "select 1"},
	}
	for _, tt := range tests {
		got, err := bindArgs(tt.stmt, tt.args)
		if err != nil {
			t.Errorf("bindArgs(%q): %v", tt.stmt, err)
		} else if got != tt.want {
			t.Errorf("bindArgs(%q) = %q, want %q", tt.stmt, got, tt.want)
		}
	}
	for _, tt := range []struct {
		stmt string
		args []any
	}{
		{"select ?", nil},
		{"select ?", []any{1, 2}},
		{"select ?", []any{struct{}{}}},
	} {
		if _, err := bindArgs(tt.stmt, tt.args); err == nil {
			t.Errorf("bindArgs(%q, %v): want an error", tt.stmt, tt.args)
		}
	}
}
