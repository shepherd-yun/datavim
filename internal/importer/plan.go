package importer

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// Column is a file column picked for import and the column it goes to.
type Column struct {
	Source int    // index in the file's rows
	Header string // the file's name for it, for messages
	Target Target
	// Generate: the column has no file column (Source is -1); the import
	// numbers the rows from the table's largest value + 1.
	Generate bool
}

// Plan says how file rows become table rows.
type Plan struct {
	Driver  string // "mysql" or "postgres"
	Table   db.TableRef
	Columns []Column
	Width   int    // the header's width
	Null    string // the text of an empty cell
}

// Row converts the file row at line. ok is false for a row whose cells are
// all blank, which is skipped. Cells past the header's width must be blank
// (a trailing delimiter); the error is a *RowError.
func (p Plan) Row(line int, cells []string) (out []Cell, ok bool, err error) {
	if blank(cells) {
		return nil, false, nil
	}
	if len(cells) > p.Width && !blank(cells[p.Width:]) {
		return nil, false, &RowError{Line: line, Reason: fmt.Sprintf("%d cells, the header has %d", len(cells), p.Width)}
	}
	out = make([]Cell, len(p.Columns))
	for i, c := range p.Columns {
		if c.Generate {
			continue // numbered by Run
		}
		text := ""
		if c.Source < len(cells) {
			text = cells[c.Source]
		}
		v, err := Convert(c.Target, text, p.Null)
		if err != nil {
			return nil, false, &RowError{Line: line, Column: c.Header, Value: text, Reason: err.Error()}
		}
		out[i] = v
	}
	return out, true, nil
}

func blank(cells []string) bool {
	for _, c := range cells {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

const (
	maxBatch  = 500
	maxParams = 65535 // MySQL's and PostgreSQL's limit on placeholders
)

// BatchSize is how many rows of cols columns one INSERT carries.
func BatchSize(cols int) int { return max(1, min(maxBatch, maxParams/max(cols, 1))) }

// Insert writes one INSERT of rows: values are arguments, NULL and DEFAULT
// are keywords.
func (p Plan) Insert(rows [][]Cell) (string, []any) {
	d := sqlx.DialectOf(p.Driver)
	var b strings.Builder
	b.WriteString("INSERT INTO " + sqlx.QualifiedName(d, p.Table) + " (")
	for i, c := range p.Columns {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(sqlx.QuoteIdent(d, c.Target.Name))
	}
	b.WriteString(") VALUES ")
	var args []any
	for i, row := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('(')
		for j, v := range row {
			if j > 0 {
				b.WriteString(", ")
			}
			switch {
			case v.Null:
				b.WriteString("NULL")
			case v.Default:
				b.WriteString("DEFAULT")
			default:
				args = append(args, v.Text)
				if d == sqlx.Postgres {
					b.WriteString("$" + strconv.Itoa(len(args)))
				} else {
					b.WriteByte('?')
				}
			}
		}
		b.WriteByte(')')
	}
	return b.String(), args
}

// Ident turns a file's column name or file name into a column or table
// name: characters other than letters (any script), digits and _ become _;
// a name with nothing else becomes fallback.
func Ident(s, fallback string) string {
	out := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return r
		}
		return '_'
	}, strings.TrimSpace(s))
	if strings.Trim(out, "_") == "" {
		return fallback
	}
	return out
}

// UniqueIdents makes column names from a header: Ident each (column_N for
// an empty one) and add _2, _3… to names taken already, ignoring case.
func UniqueIdents(names []string) []string {
	out := make([]string, len(names))
	taken := map[string]bool{}
	for i, n := range names {
		base := Ident(n, "column_"+strconv.Itoa(i+1))
		name := base
		for k := 2; taken[strings.ToLower(name)]; k++ {
			name = base + "_" + strconv.Itoa(k)
		}
		taken[strings.ToLower(name)] = true
		out[i] = name
	}
	return out
}

// MatchKey is what file columns are matched to table columns by: case and
// the separators _, - and space do not count.
func MatchKey(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '_', '-', ' ':
			return -1
		}
		return unicode.ToLower(r)
	}, strings.TrimSpace(s))
}
