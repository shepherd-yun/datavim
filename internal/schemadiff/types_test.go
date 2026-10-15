package schemadiff

import "testing"

func TestLossyType(t *testing.T) {
	for _, c := range []struct {
		old, nw string
		want    bool
	}{
		{"int", "int", false},
		{"int", "bigint", false},
		{"bigint", "int", true},
		{"tinyint(1)", "int", false},
		{"int unsigned", "bigint", false},
		{"int unsigned", "int", true},
		{"int", "int unsigned", true},
		{"varchar(100)", "varchar(255)", false},
		{"varchar(255)", "varchar(100)", true},
		{"varchar(100)", "text", false},
		{"text", "varchar(100)", true},
		{"char(10)", "varchar(10)", false},
		{"varchar(10)", "char(10)", true},
		{"decimal(10,2)", "decimal(12,2)", false},
		{"decimal(10,2)", "decimal(12,4)", false},
		{"decimal(10,2)", "decimal(10,3)", true},
		{"numeric(10,2)", "numeric", false},
		{"numeric", "numeric(10,2)", true},
		{"float", "double", false},
		{"double", "float", true},
		{"timestamp(3)", "timestamp(6)", false},
		{"timestamp(6)", "timestamp(3)", true},
		{"int[]", "bigint[]", false},
		{"int", "int[]", true},
		{"varchar(10)", "int", true},
		{"enum('a','b')", "enum('a','b','c')", true},
	} {
		if got := LossyType(c.old, c.nw); got != c.want {
			t.Errorf("LossyType(%q, %q) = %v, want %v", c.old, c.nw, got, c.want)
		}
	}
}
