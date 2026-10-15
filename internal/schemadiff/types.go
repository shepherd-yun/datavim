package schemadiff

import (
	"math"
	"strconv"
	"strings"
)

// parsedType is a normalized column type split up: varchar(100) is base
// varchar with args [100].
type parsedType struct {
	base     string
	args     []int
	unsigned bool
	array    bool
}

func parseType(t string) parsedType {
	var p parsedType
	t, p.array = strings.CutSuffix(t, "[]")
	t, p.unsigned = strings.CutSuffix(t, " unsigned")
	p.base = t
	open := strings.IndexByte(t, '(')
	if open <= 0 || !strings.HasSuffix(t, ")") {
		return p
	}
	var args []int
	for _, a := range strings.Split(t[open+1:len(t)-1], ",") {
		n, err := strconv.Atoi(strings.TrimSpace(a))
		if err != nil {
			return p // enum('a','b') and the like stay whole
		}
		args = append(args, n)
	}
	p.base, p.args = t[:open], args
	return p
}

var (
	intRank      = map[string]int{"tinyint": 1, "smallint": 2, "mediumint": 3, "int": 4, "bigint": 5}
	textCapacity = map[string]int{"tinytext": 255, "text": 65535, "mediumtext": 16777215, "longtext": math.MaxInt}
	wider        = map[[2]string]bool{{"float", "double"}: true, {"real", "double precision"}: true, {"real", "double"}: true}
)

func isText(base string) bool { return base == "char" || base == "varchar" || textCapacity[base] > 0 }

func isDecimal(base string) bool { return base == "decimal" || base == "numeric" }

// capacity is how many characters a text type holds.
func capacity(p parsedType) int {
	switch {
	case p.base == "char" && len(p.args) > 0, p.base == "varchar" && len(p.args) > 0:
		return p.args[0]
	case p.base == "char":
		return 1
	case p.base == "varchar":
		return math.MaxInt
	}
	return textCapacity[p.base]
}

// LossyType reports whether changing a column from type old to type nw (both
// normalized) can reject or cut values the column holds. Changes it does not
// understand count as lossy.
func LossyType(old, nw string) bool {
	if old == nw {
		return false
	}
	o, n := parseType(old), parseType(nw)
	if o.array != n.array {
		return true
	}
	if ro, rn := intRank[o.base], intRank[n.base]; ro > 0 && rn > 0 {
		switch {
		case o.unsigned == n.unsigned:
			return rn < ro
		case o.unsigned: // unsigned → signed needs a wider type
			return rn <= ro
		default: // signed → unsigned loses negative values
			return true
		}
	}
	if o.unsigned != n.unsigned {
		return true
	}
	switch {
	case isText(o.base) && isText(n.base):
		return n.base == "char" && o.base != "char" || capacity(n) < capacity(o)
	case isDecimal(o.base) && isDecimal(n.base):
		if len(n.args) == 0 {
			return false // unbounded
		}
		if len(o.args) == 0 {
			return true
		}
		scale := func(p parsedType) int {
			if len(p.args) > 1 {
				return p.args[1]
			}
			return 0
		}
		return n.args[0]-scale(n) < o.args[0]-scale(o) || scale(n) < scale(o)
	case o.base == n.base:
		if len(o.args) != len(n.args) {
			return true
		}
		for i := range o.args {
			if n.args[i] < o.args[i] {
				return true
			}
		}
		return false
	}
	return !wider[[2]string{o.base, n.base}]
}
