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
	s := q.sql(d, ref)
	if len(sqlx.Split(s, d)) != 1 || sqlx.Analyze(s, d).Kind != sqlx.Read {
		return errBadFilter
	}
	return nil
}

// valueFilter matches rows whose col equals v.
func valueFilter(d sqlx.Dialect, col db.ColumnInfo, v db.Value) string {
	name := sqlx.QuoteIdent(d, col.Name)
	if v.Null {
		return name + " IS NULL"
	}
	return name + " = " + sqlx.Literal(d, col.DatabaseType, v)
}

// andFilter narrows the filter old with cond.
func andFilter(old, cond string) string {
	if old == "" {
		return cond
	}
	return "(" + old + ") AND " + cond
}
