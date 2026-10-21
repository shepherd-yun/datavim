package edit

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// Target is the table staged changes are written to.
type Target struct {
	Dialect sqlx.Dialect
	Table   db.TableRef
	Key     Key
	// Columns names the table column behind each result column (a console
	// result may rename them); nil when they are the same.
	Columns []string
}

// column is result column i under its table column's name.
func (tg Target) column(cols []db.ColumnInfo, i int) db.ColumnInfo {
	c := cols[i]
	if tg.Columns != nil {
		c.Name = tg.Columns[i]
	}
	return c
}

// ErrNoKey means a changed or deleted row cannot be located.
var ErrNoKey = errors.New("no primary key, unique NOT NULL index or row id: rows can only be added")

// incomparable types do not reliably match their text with =, so
// AllColumns leaves them out of the WHERE clause. SQL Server cannot compare
// its legacy and CLR types with = at all.
var (
	incomparable     = map[string]bool{"FLOAT": true, "DOUBLE": true, "REAL": true, "JSON": true, "GEOMETRY": true}
	incomparableTSQL = map[string]bool{
		"TEXT": true, "NTEXT": true, "IMAGE": true, "XML": true, "GEOGRAPHY": true, "GEOMETRY": true,
		"HIERARCHYID": true, "SQL_VARIANT": true,
	}
)

func matchable(d sqlx.Dialect, dbType string) bool {
	t := sqlx.NormType(dbType)
	return !incomparable[t] && (d != sqlx.SQLServer || !incomparableTSQL[t])
}

// Plan turns the staged changes into statements: deletes, then updates,
// then inserts, each in row order. Rows are located by the values read
// from the server. warnings explain a weak way of locating rows.
func Plan(t *Table, tg Target) (stmts, warnings []string, err error) {
	d := tg.Dialect
	name := sqlx.QualifiedName(d, tg.Table)
	var dels, upds, ins []string
	for _, r := range t.rows {
		switch r.State {
		case Deleted:
			where, err := tg.where(t.cols, r)
			if err != nil {
				return nil, nil, err
			}
			dels = append(dels, "DELETE "+tg.top()+"FROM "+name+" WHERE "+where+tg.limit())
		case Modified:
			where, err := tg.where(t.cols, r)
			if err != nil {
				return nil, nil, err
			}
			var sets []string
			for i := range t.cols {
				if r.Set[i] {
					c := tg.column(t.cols, i)
					sets = append(sets, sqlx.QuoteIdent(d, c.Name)+" = "+sqlx.Literal(d, c.DatabaseType, r.Values[i]))
				}
			}
			upds = append(upds, "UPDATE "+tg.top()+name+" SET "+strings.Join(sets, ", ")+" WHERE "+where+tg.limit())
		case Inserted:
			ins = append(ins, tg.insert(name, t.cols, r))
		}
	}
	if len(dels)+len(upds) > 0 {
		if w := tg.warning(); w != "" {
			warnings = append(warnings, w)
		}
	}
	return slices.Concat(dels, upds, ins), warnings, nil
}

// where locates r by its server values.
func (tg Target) where(cols []db.ColumnInfo, r *Row) (string, error) {
	d := tg.Dialect
	var parts []string
	switch tg.Key.Kind {
	case RowID:
		if r.ID == "" {
			return "", ErrNoKey
		}
		return sqlx.RowIDMatch(d, r.ID), nil
	case PrimaryKey, UniqueKey:
		for _, name := range tg.Key.Columns {
			i := slices.IndexFunc(cols, func(c db.ColumnInfo) bool { return c.Name == name })
			if tg.Columns != nil {
				i = slices.Index(tg.Columns, name)
			}
			if i < 0 {
				return "", fmt.Errorf("key column %s is not in the result", name)
			}
			parts = append(parts, match(d, tg.column(cols, i), r.Orig[i]))
		}
	case AllColumns:
		for i := range cols {
			c := tg.column(cols, i)
			if matchable(d, c.DatabaseType) {
				parts = append(parts, match(d, c, r.Orig[i]))
			}
		}
	}
	if len(parts) == 0 {
		return "", ErrNoKey
	}
	return strings.Join(parts, " AND "), nil
}

func match(d sqlx.Dialect, c db.ColumnInfo, v db.Value) string {
	col := sqlx.QuoteIdent(d, c.Name)
	if v.Null {
		return col + " IS NULL"
	}
	return col + " = " + sqlx.Literal(d, c.DatabaseType, v)
}

// limit and top keep a statement located by all its values to one row:
// LIMIT 1 at the end, or TOP (1) after the verb on SQL Server.
func (tg Target) limit() string {
	if tg.Key.Kind == AllColumns && tg.Dialect != sqlx.SQLServer {
		return " LIMIT 1"
	}
	return ""
}

func (tg Target) top() string {
	if tg.Key.Kind == AllColumns && tg.Dialect == sqlx.SQLServer {
		return "TOP (1) "
	}
	return ""
}

func (tg Target) warning() string {
	switch tg.Key.Kind {
	case AllColumns:
		one := "LIMIT 1"
		if tg.Dialect == sqlx.SQLServer {
			one = "TOP (1)"
		}
		return tg.Table.Name + " has no primary key or unique NOT NULL index: rows are matched by all their values, one row at most (" + one + ")"
	case RowID:
		return tg.Table.Name + " has no primary key or unique NOT NULL index: rows are matched by their " + sqlx.RowIDName(tg.Dialect)
	}
	return ""
}

// insert writes the columns given a value; the rest keep their defaults.
func (tg Target) insert(name string, cols []db.ColumnInfo, r *Row) string {
	d := tg.Dialect
	var names, vals []string
	for i := range cols {
		if r.Set[i] {
			c := tg.column(cols, i)
			names = append(names, sqlx.QuoteIdent(d, c.Name))
			vals = append(vals, sqlx.Literal(d, c.DatabaseType, r.Values[i]))
		}
	}
	if len(names) > 0 {
		return "INSERT INTO " + name + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(vals, ", ") + ")"
	}
	//exhaustive:enforce
	switch d {
	case sqlx.MySQL:
		return "INSERT INTO " + name + " () VALUES ()"
	case sqlx.Oracle: // no DEFAULT VALUES: give the first column its default
		if len(cols) > 0 {
			return "INSERT INTO " + name + " (" + sqlx.QuoteIdent(d, tg.column(cols, 0).Name) + ") VALUES (DEFAULT)"
		}
	case sqlx.Postgres, sqlx.SQLServer:
	}
	return "INSERT INTO " + name + " DEFAULT VALUES"
}
