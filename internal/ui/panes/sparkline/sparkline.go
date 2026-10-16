// Package sparkline draws a series of numbers as one line of block characters.
package sparkline

import (
	"math"
	"strings"
)

var levels = []rune("▁▂▃▄▅▆▇█")

// Render draws the last width values with ▁▂▃▄▅▆▇█, right-aligned (padded
// with spaces on the left). fromZero scales 0..max, else min..max; a flat
// or empty series is all ▁ (no values: all spaces).
func Render(vals []float64, width int, fromZero bool) string {
	if width <= 0 {
		return ""
	}
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range vals {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if fromZero {
		lo = 0
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", width-len(vals)))
	top := len(levels) - 1
	for _, v := range vals {
		i := 0
		if hi > lo {
			i = int(math.Round((v - lo) / (hi - lo) * float64(top)))
			i = min(max(i, 0), top)
		}
		b.WriteRune(levels[i])
	}
	return b.String()
}
