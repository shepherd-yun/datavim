package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/shepherd-yun/datavim/internal/db"
)

const errNoSuchRoutine = 1305

var systemDatabases = []string{"information_schema", "mysql", "performance_schema", "sys"}

// userDatabases filters an information_schema *_SCHEMA column.
const userDatabases = " NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')"

// each runs q and calls scan for every row.
func (m meta) each(ctx context.Context, q string, scan func(*sql.Rows) error) error {
	rows, err := m.c.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (m meta) Catalog(ctx context.Context) (db.Catalog, error) {
	var c db.Catalog
	var err error
	if c.Databases, err = m.Databases(ctx); err != nil {
		return c, err
	}
	for _, d := range c.Databases {
		if !slices.Contains(systemDatabases, strings.ToLower(d)) {
			c.Namespaces = append(c.Namespaces, db.Namespace{Database: d})
		}
	}
	err = m.each(ctx, `SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES
		WHERE TABLE_SCHEMA`+userDatabases+` ORDER BY TABLE_SCHEMA, TABLE_NAME`, func(rows *sql.Rows) error {
		var t db.CatalogTable
		var typ string
		if err := rows.Scan(&t.Namespace.Database, &t.Name, &typ); err != nil {
			return err
		}
		if strings.Contains(typ, "VIEW") {
			t.Kind = db.TableKindView
		}
		c.Tables = append(c.Tables, t)
		return nil
	})
	if err != nil {
		return c, err
	}
	err = m.each(ctx, `SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, COLUMN_TYPE FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA`+userDatabases+` ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION`, func(rows *sql.Rows) error {
		var col db.CatalogColumn
		if err := rows.Scan(&col.Table.Database, &col.Table.Name, &col.Name, &col.Type); err != nil {
			return err
		}
		c.Columns = append(c.Columns, col)
		return nil
	})
	if err != nil {
		return c, err
	}
	err = m.each(ctx, `SELECT ROUTINE_SCHEMA, ROUTINE_NAME, ROUTINE_TYPE FROM information_schema.ROUTINES
		WHERE ROUTINE_SCHEMA`+userDatabases+` ORDER BY ROUTINE_SCHEMA, ROUTINE_NAME`, func(rows *sql.Rows) error {
		var r db.Routine
		var typ string
		if err := rows.Scan(&r.Namespace.Database, &r.Name, &typ); err != nil {
			return err
		}
		if typ == "PROCEDURE" {
			r.Kind = db.RoutineProcedure
		}
		c.Routines = append(c.Routines, r)
		return nil
	})
	return c, err
}

func (m meta) RoutineDDL(ctx context.Context, r db.Routine) (string, error) {
	kind := "FUNCTION"
	if r.Kind == db.RoutineProcedure {
		kind = "PROCEDURE"
	}
	d := dialect{}
	name := d.QuoteIdent(r.Namespace.Database) + "." + d.QuoteIdent(r.Name)
	notFound := fmt.Errorf("%s: %w", name, db.ErrNotFound)
	rows, err := m.c.db.QueryContext(ctx, "SHOW CREATE "+kind+" "+name)
	if err != nil {
		var me *gomysql.MySQLError
		if errors.As(err, &me) && me.Number == errNoSuchRoutine {
			return "", notFound
		}
		return "", err
	}
	defer func() { _ = rows.Close() }()
	vals, ok, err := firstRow(rows)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", notFound
	}
	// (Function, sql_mode, Create Function, ...); the definition is NULL
	// when the user may not read it.
	if len(vals) < 3 || !vals[2].Valid {
		return "", fmt.Errorf("%s: no privilege to read the definition", name)
	}
	return vals[2].String, nil
}

// firstRow scans the first row as strings; ok is false when there is none.
func firstRow(rows *sql.Rows) (vals []sql.NullString, ok bool, err error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, false, err
	}
	if !rows.Next() {
		return nil, false, rows.Err()
	}
	vals = make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	return vals, true, rows.Scan(ptrs...)
}
