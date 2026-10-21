package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/shepherd-yun/datavim/internal/db"
)

const errNoSuchTable = 1146

type meta struct{ c *conn }

func (m meta) Databases(ctx context.Context) ([]string, error) {
	rows, err := m.c.db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (meta) Schemas(context.Context, string) ([]string, error) { return nil, nil }

func (m meta) Tables(ctx context.Context, ns db.Namespace) ([]db.Table, error) {
	rows, err := m.c.db.QueryContext(ctx, `
		SELECT TABLE_NAME, TABLE_TYPE, TABLE_COMMENT FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, ns.Database)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []db.Table
	for rows.Next() {
		var t db.Table
		var typ string
		var comment sql.NullString
		if err := rows.Scan(&t.Name, &typ, &comment); err != nil {
			return nil, err
		}
		if strings.Contains(typ, "VIEW") {
			t.Kind = db.TableKindView
		} else {
			t.Comment = comment.String
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (m meta) Columns(ctx context.Context, t db.TableRef) ([]db.Column, error) {
	rows, err := m.c.db.QueryContext(ctx, `
		SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_COMMENT, COLUMN_KEY
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, t.Database, t.Name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []db.Column
	for rows.Next() {
		var c db.Column
		var nullable, key string
		var def sql.NullString
		if err := rows.Scan(&c.Name, &c.Type, &nullable, &def, &c.Comment, &key); err != nil {
			return nil, err
		}
		c.Nullable = nullable == "YES"
		c.PrimaryKey = key == "PRI"
		if def.Valid {
			c.Default = &def.String
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m meta) Indexes(ctx context.Context, t db.TableRef) ([]db.Index, error) {
	rows, err := m.c.db.QueryContext(ctx, `
		SELECT INDEX_NAME, NON_UNIQUE, COALESCE(COLUMN_NAME, '(expression)')
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY INDEX_NAME, SEQ_IN_INDEX`, t.Database, t.Name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []db.Index
	for rows.Next() {
		var name, col string
		var nonUnique int
		if err := rows.Scan(&name, &nonUnique, &col); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].Name == name {
			out[n-1].Columns = append(out[n-1].Columns, col)
			continue
		}
		out = append(out, db.Index{Name: name, Columns: []string{col}, Unique: nonUnique == 0, Primary: name == "PRIMARY"})
	}
	return out, rows.Err()
}

func (m meta) ForeignKeys(ctx context.Context, t db.TableRef) ([]db.ForeignKey, error) {
	rows, err := m.c.db.QueryContext(ctx, `
		SELECT CONSTRAINT_NAME, COLUMN_NAME, REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
		FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY CONSTRAINT_NAME, ORDINAL_POSITION`, t.Database, t.Name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []db.ForeignKey
	for rows.Next() {
		var name, col, refDB, refTable, refCol string
		if err := rows.Scan(&name, &col, &refDB, &refTable, &refCol); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].Name == name {
			out[n-1].Columns = append(out[n-1].Columns, col)
			out[n-1].RefColumns = append(out[n-1].RefColumns, refCol)
			continue
		}
		out = append(out, db.ForeignKey{
			Name: name, Columns: []string{col},
			RefTable:   db.TableRef{Namespace: db.Namespace{Database: refDB}, Name: refTable},
			RefColumns: []string{refCol},
		})
	}
	return out, rows.Err()
}

func (m meta) DDL(ctx context.Context, t db.TableRef) (string, error) {
	name := dialect{}.QualifiedName(t)
	notFound := fmt.Errorf("%s: %w", name, db.ErrNotFound)
	rows, err := m.c.db.QueryContext(ctx, "SHOW CREATE TABLE "+name)
	if err != nil {
		var me *gomysql.MySQLError
		if errors.As(err, &me) && me.Number == errNoSuchTable {
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
	// Tables return (Table, Create Table); views return (View, Create View, ...).
	return vals[1].String, nil
}
