package ui

import (
	"errors"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// tableQuery is what a data tab shows: its filter and sort, the key that
// keeps pages stable and whether rows carry their ctid.
type tableQuery struct {
	where string   // the filter without WHERE; "" for every row
	sort  string   // the column sorted by; "" for key order
	desc  bool     // sort descending
	key   []string // ORDER BY after the sort column
	rowID bool     // PostgreSQL: select ctid::text first
}

var errBadFilter = errors.New("the filter must be a condition, like: name = 'x' AND id > 10")

// sql is the query without pagination. The filter is closed on its own
// line, so a trailing -- comment cannot swallow ORDER BY or LIMIT.
func (q tableQuery) sql(d sqlx.Dialect, ref db.TableRef) string {
	sel := "SELECT * FROM "
	if q.rowID {
		sel = "SELECT ctid::text, * FROM "
	}
	s := sel + sqlx.QualifiedName(d, ref)
	if q.where != "" {
		s += " WHERE (" + q.where + "\n)"
	}
	var order []string
	if q.sort != "" {
		o := sqlx.QuoteIdent(d, q.sort)
		if q.desc {
			o += " DESC"
		}
		order = append(order, o)
	}
	for _, k := range q.key {
		if k != q.sort {
			order = append(order, sqlx.QuoteIdent(d, k))
		}
	}
	if len(order) > 0 {
		s += " ORDER BY " + strings.Join(order, ", ")
	}
	return s
}

// check rejects a filter that turns the query into more than one SELECT.
func (q tableQuery) check(d sqlx.Dialect, ref db.TableRef) error {
	return checkSelect(d, q.sql(d, ref))
}

// checkSelect rejects a query that a typed filter turned into more than
// one statement, or into something other than a read.
func checkSelect(d sqlx.Dialect, q string) error {
	if len(sqlx.Split(q, d)) != 1 || sqlx.Analyze(q, d).Kind != sqlx.Read {
		return errBadFilter
	}
	return nil
}

// compareFilter is col op v, op being = <> > or <; a NULL v turns = and <>
// into IS NULL and IS NOT NULL.
func compareFilter(d sqlx.Dialect, col db.ColumnInfo, op string, v db.Value) string {
	if v.Null {
		return nullFilter(d, col.Name, op == "<>")
	}
	return sqlx.QuoteIdent(d, col.Name) + " " + op + " " + sqlx.Literal(d, col.DatabaseType, v)
}

// nullFilter is col IS NULL, or IS NOT NULL with not.
func nullFilter(d sqlx.Dialect, col string, not bool) string {
	if not {
		return sqlx.QuoteIdent(d, col) + " IS NOT NULL"
	}
	return sqlx.QuoteIdent(d, col) + " IS NULL"
}

// likeEscaper makes % _ and \ match themselves in a LIKE pattern (\ is the
// default escape on MySQL and PostgreSQL).
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// containsFilter matches rows whose col contains s, ignoring case: ILIKE on
// PostgreSQL, with ::text so numbers, dates and uuids match too, and LIKE
// on MySQL, whose default collations ignore case.
func containsFilter(d sqlx.Dialect, col, s string) string {
	return likeFilter(d, col, "%"+likeEscaper.Replace(s)+"%")
}

// likeFilter matches col against a LIKE pattern as typed, % and _ being
// wildcards; it ignores case like containsFilter.
func likeFilter(d sqlx.Dialect, col, pattern string) string {
	lit := sqlx.Literal(d, "TEXT", db.Value{Text: pattern})
	if d.Features().ILike {
		return sqlx.QuoteIdent(d, col) + "::text ILIKE " + lit
	}
	return sqlx.QuoteIdent(d, col) + " LIKE " + lit
}

// andFilter narrows the filter old with cond.
func andFilter(old, cond string) string {
	if old == "" {
		return cond
	}
	return "(" + old + ") AND " + cond
}
