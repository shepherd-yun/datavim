package sqlserver

import (
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
)

// buildTableDDL writes a CREATE TABLE with its primary key, followed by
// its other indexes and foreign keys, from what TableSchema read.
func buildTableDDL(s db.TableSchema) string {
	d := dialect{}
	name := d.QualifiedName(s.Ref)
	quote := func(cols []string) string {
		q := make([]string, len(cols))
		for i, c := range cols {
			q[i] = d.QuoteIdent(c)
		}
		return strings.Join(q, ", ")
	}
	var lines []string
	for _, c := range s.Columns {
		l := "    " + d.QuoteIdent(c.Name) + " " + c.Type
		if !c.Nullable {
			l += " NOT NULL"
		}
		if c.Default != nil {
			l += " DEFAULT " + *c.Default
		}
		lines = append(lines, l)
	}
	var after []string
	for _, ix := range s.Indexes {
		switch {
		case ix.Primary:
			lines = append(lines, "    CONSTRAINT "+d.QuoteIdent(ix.Name)+" PRIMARY KEY ("+quote(ix.Columns)+")")
		case ix.Unique:
			after = append(after, "CREATE UNIQUE INDEX "+d.QuoteIdent(ix.Name)+" ON "+name+" ("+quote(ix.Columns)+");")
		default:
			after = append(after, "CREATE INDEX "+d.QuoteIdent(ix.Name)+" ON "+name+" ("+quote(ix.Columns)+");")
		}
	}
	for _, fk := range s.ForeignKeys {
		after = append(after, "ALTER TABLE "+name+" ADD CONSTRAINT "+d.QuoteIdent(fk.Name)+
			" FOREIGN KEY ("+quote(fk.Columns)+") REFERENCES "+d.QualifiedName(fk.RefTable)+" ("+quote(fk.RefColumns)+");")
	}
	out := "CREATE TABLE " + name + " (\n" + strings.Join(lines, ",\n") + "\n);"
	if len(after) > 0 {
		out += "\n\n" + strings.Join(after, "\n")
	}
	return out
}
