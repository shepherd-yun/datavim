package postgres

import (
	"fmt"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
)

type constraint struct {
	name string
	def  string
}

// buildTableDDL assembles CREATE TABLE text; PostgreSQL has no SHOW CREATE TABLE.
func buildTableDDL(qualified string, cols []db.Column, cons []constraint, indexDefs []string) string {
	q := dialect{}
	lines := make([]string, 0, len(cols)+len(cons))
	for _, c := range cols {
		l := "    " + q.QuoteIdent(c.Name) + " " + c.Type
		if !c.Nullable {
			l += " NOT NULL"
		}
		if c.Default != nil {
			l += " DEFAULT " + *c.Default
		}
		lines = append(lines, l)
	}
	for _, c := range cons {
		lines = append(lines, "    CONSTRAINT "+q.QuoteIdent(c.name)+" "+c.def)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n%s\n);\n", qualified, strings.Join(lines, ",\n"))
	for _, d := range indexDefs {
		b.WriteString(d + ";\n")
	}
	return b.String()
}
