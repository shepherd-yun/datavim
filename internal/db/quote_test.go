package db

import "testing"

func TestQuote(t *testing.T) {
	tests := []struct {
		in   string
		q    rune
		want string
	}{
		{"users", '"', `"users"`},
		{"MixedCase", '"', `"MixedCase"`},
		{`we"ird`, '"', `"we""ird"`},
		{"order", '`', "`order`"},
		{"a`b", '`', "`a``b`"},
		{"表", '`', "`表`"},
	}
	for _, tt := range tests {
		if got := Quote(tt.in, tt.q); got != tt.want {
			t.Errorf("Quote(%q, %q) = %s, want %s", tt.in, tt.q, got, tt.want)
		}
	}
}

func TestLimitOffset(t *testing.T) {
	got := LimitOffset("SELECT * FROM t", 201, 400)
	if want := "SELECT * FROM t LIMIT 201 OFFSET 400"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
