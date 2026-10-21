package highlight

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// bigConsole is a console of n statements over about 3n lines.
func bigConsole(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString("-- query\nSELECT id, name, 'x' FROM users\nWHERE id = ")
		b.WriteString(strings.Repeat("1", i%7+1))
		b.WriteString(";\n")
	}
	return b.String()
}

func BenchmarkSQLWholeText(b *testing.B) {
	src := bigConsole(2000)
	for b.Loop() {
		SQL(src+"x", sqlx.MySQL)
	}
}

func BenchmarkHighlighterTyping(b *testing.B) {
	src := bigConsole(2000)
	h := NewHighlighter(sqlx.MySQL)
	h.Spans(src)
	i := 0
	for b.Loop() {
		i++
		h.Spans(src + strings.Repeat("x", i%5))
	}
}
