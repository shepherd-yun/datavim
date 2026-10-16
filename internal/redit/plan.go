// Package redit turns staged changes to a Redis key into the commands
// that apply them and the checks that must hold first (design §6.2). Pure.
package redit

import (
	"strconv"
)

// Expect is the kind of reply a Check wants.
type Expect int

const (
	ExpectValue Expect = iota // a bulk string equal to Value
	ExpectNil                 // nil: the field / member / key is missing
	ExpectInt                 // an integer equal to Int
	ExpectScore               // a score equal to Score (RESP2 string or RESP3 double)
	ExpectLen                 // an array with Int elements
)

// Check is a read command whose reply must be as expected before the
// plan's commands run.
type Check struct {
	Args   []string
	Expect Expect
	Value  string
	Int    int64
	Score  float64
	What   string // why it failing matters, e.g. `field "a" changed`; shown after " · "
}

// Holds reports whether reply v (nil, string, int64, float64, []any, or an
// error) is what c expects. An error never holds.
func (c Check) Holds(v any) bool {
	if _, ok := v.(error); ok {
		return false
	}
	switch c.Expect {
	case ExpectValue:
		s, ok := v.(string)
		return ok && s == c.Value
	case ExpectNil:
		return v == nil
	case ExpectInt:
		n, ok := v.(int64)
		return ok && n == c.Int
	case ExpectScore:
		switch x := v.(type) {
		case float64:
			return x == c.Score
		case string:
			f, err := strconv.ParseFloat(x, 64)
			return err == nil && f == c.Score
		}
	case ExpectLen:
		a, ok := v.([]any)
		return ok && int64(len(a)) == c.Int
	}
	return false
}

// Plan is what applying staged changes to one key takes.
type Plan struct {
	Key        string // WATCHed
	Checks     []Check
	Cmds       [][]string // run in MULTI … EXEC, in order
	RestoreTTL bool       // Redis < 6: PEXPIRE the PTTL read after the checks, after the SET
	N          int        // changes it applies, for the preview's title ("Submit N changes")
}
