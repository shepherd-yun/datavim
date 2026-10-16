package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

type meta struct{ c *conn }

// systemDatabases come last in Databases.
const systemDatabases = `('master', 'model', 'msdb', 'tempdb')`

// userSchemas filters sys.schemas s down to the schemas Schemas lists:
// no system schemas, no fixed database roles.
const userSchemas = `s.name NOT IN ('sys', 'INFORMATION_SCHEMA', 'guest') AND s.name NOT LIKE 'db[_]%'`

// objName is t as OBJECT_ID reads it, within the Conn's database.
func objName(t db.TableRef) string { return dialect{}.QualifiedName(t) }

func (m meta) strings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := m.c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrap(err)
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

// each runs query and calls fn after scanning each row into dest.
func (m meta) each(ctx context.Context, query string, args []any, dest []any, fn func()) error {
	rows, err := m.c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return wrap(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		fn()
	}
	return rows.Err()
}

func (m meta) Databases(ctx context.Context) ([]string, error) {
	return m.strings(ctx, `SELECT name FROM sys.databases WHERE HAS_DBACCESS(name) = 1
		ORDER BY CASE WHEN name IN `+systemDatabases+` THEN 1 ELSE 0 END, name`)
}

// current is the Conn's database.
func (m meta) current(ctx context.Context) (string, error) {
	var name string
	err := m.c.db.QueryRowContext(ctx, `SELECT DB_NAME()`).Scan(&name)
	return name, wrap(err)
}

func (m meta) checkDatabase(ctx context.Context, database string) error {
	if database == "" {
		return nil
	}
	cur, err := m.current(ctx)
	if err != nil {
		return err
	}
	if !strings.EqualFold(cur, database) {
		return fmt.Errorf("sqlserver: connection is bound to database %q, not %q", cur, database)
	}
	return nil
}

func (m meta) Schemas(ctx context.Context, database string) ([]string, error) {
	if err := m.checkDatabase(ctx, database); err != nil {
		return nil, err
	}
	return m.strings(ctx, `SELECT s.name FROM sys.schemas s WHERE `+userSchemas+`
		ORDER BY CASE WHEN s.name = 'dbo' THEN 0 ELSE 1 END, s.name`)
}

// description reads MS_Description, the comment SSMS shows.
const description = `CAST(ISNULL((SELECT TOP 1 ep.value FROM sys.extended_properties ep
	WHERE ep.class = 1 AND ep.name = 'MS_Description' AND ep.major_id = %s AND ep.minor_id = %s), '') AS nvarchar(max))`

func (m meta) Tables(ctx context.Context, ns db.Namespace) ([]db.Table, error) {
	if err := m.checkDatabase(ctx, ns.Database); err != nil {
		return nil, err
	}
	var out []db.Table
	var t db.Table
	var kind string
	err := m.each(ctx, `SELECT o.name, o.type, `+fmt.Sprintf(description, "o.object_id", "0")+`
		FROM sys.objects o JOIN sys.schemas s ON s.schema_id = o.schema_id
		WHERE s.name = @p1 AND o.type IN ('U', 'V') ORDER BY o.name`,
		[]any{ns.Schema}, []any{&t.Name, &kind, &t.Comment}, func() {
			t.Kind = db.TableKindTable
			if strings.TrimSpace(kind) == "V" {
				t.Kind = db.TableKindView
			}
			out = append(out, t)
		})
	return out, err
}

// typeName spells a sys.types name with its length, precision or scale.
// max_length counts bytes: nchar and nvarchar use two per character.
func typeName(name string, maxLen, prec, scale int) string {
	switch name {
	case "varchar", "char", "varbinary", "binary", "nvarchar", "nchar":
		if maxLen == -1 {
			return name + "(max)"
		}
		if name == "nvarchar" || name == "nchar" {
			maxLen /= 2
		}
		return name + "(" + strconv.Itoa(maxLen) + ")"
	case "decimal", "numeric":
		return fmt.Sprintf("%s(%d,%d)", name, prec, scale)
	case "datetime2", "time", "datetimeoffset":
		return fmt.Sprintf("%s(%d)", name, scale)
	}
	return name
}

func (m meta) Columns(ctx context.Context, t db.TableRef) ([]db.Column, error) {
	var out []db.Column
	var c db.Column
	var typ string
	var maxLen, prec, scale int
	var def sql.NullString
	err := m.each(ctx, `SELECT c.name, ty.name, c.max_length, c.precision, c.scale, c.is_nullable,
		       OBJECT_DEFINITION(c.default_object_id), `+fmt.Sprintf(description, "c.object_id", "c.column_id")+`,
		       CAST(CASE WHEN EXISTS (SELECT 1 FROM sys.indexes i
		            JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		            WHERE i.object_id = c.object_id AND i.is_primary_key = 1 AND ic.column_id = c.column_id)
		       THEN 1 ELSE 0 END AS bit)
		FROM sys.columns c JOIN sys.types ty ON ty.user_type_id = c.user_type_id
		WHERE c.object_id = OBJECT_ID(@p1) ORDER BY c.column_id`,
		[]any{objName(t)}, []any{&c.Name, &typ, &maxLen, &prec, &scale, &c.Nullable, &def, &c.Comment, &c.PrimaryKey},
		func() {
			c.Type = typeName(typ, maxLen, prec, scale)
			c.Default = nil
			if def.Valid {
				d := def.String
				c.Default = &d
			}
			out = append(out, c)
		})
	return out, err
}

func (m meta) Indexes(ctx context.Context, t db.TableRef) ([]db.Index, error) {
	var out []db.Index
	var name, col string
	var unique, primary bool
	err := m.each(ctx, `SELECT i.name, i.is_unique, i.is_primary_key, c.name
		FROM sys.indexes i
		JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		WHERE i.object_id = OBJECT_ID(@p1) AND i.name IS NOT NULL AND ic.is_included_column = 0
		ORDER BY i.name, ic.key_ordinal`,
		[]any{objName(t)}, []any{&name, &unique, &primary, &col}, func() {
			if n := len(out); n > 0 && out[n-1].Name == name {
				out[n-1].Columns = append(out[n-1].Columns, col)
				return
			}
			out = append(out, db.Index{Name: name, Unique: unique, Primary: primary, Columns: []string{col}})
		})
	return out, err
}

func (m meta) ForeignKeys(ctx context.Context, t db.TableRef) ([]db.ForeignKey, error) {
	var out []db.ForeignKey
	var name, col, refSchema, refTable, refCol string
	err := m.each(ctx, `SELECT fk.name, pc.name, rs.name, rt.name, rc.name
		FROM sys.foreign_keys fk
		JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
		JOIN sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
		JOIN sys.objects rt ON rt.object_id = fkc.referenced_object_id
		JOIN sys.schemas rs ON rs.schema_id = rt.schema_id
		JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
		WHERE fk.parent_object_id = OBJECT_ID(@p1)
		ORDER BY fk.name, fkc.constraint_column_id`,
		[]any{objName(t)}, []any{&name, &col, &refSchema, &refTable, &refCol}, func() {
			if n := len(out); n > 0 && out[n-1].Name == name {
				out[n-1].Columns = append(out[n-1].Columns, col)
				out[n-1].RefColumns = append(out[n-1].RefColumns, refCol)
				return
			}
			out = append(out, db.ForeignKey{
				Name: name, Columns: []string{col}, RefColumns: []string{refCol},
				RefTable: db.TableRef{Namespace: db.Namespace{Database: t.Database, Schema: refSchema}, Name: refTable},
			})
		})
	return out, err
}

// kind is the sys.objects type of t, or ErrNotFound.
func (m meta) kind(ctx context.Context, t db.TableRef) (string, error) {
	var kind string
	err := m.c.db.QueryRowContext(ctx, `SELECT type FROM sys.objects WHERE object_id = OBJECT_ID(@p1)`, objName(t)).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%s: %w", objName(t), db.ErrNotFound)
	}
	return strings.TrimSpace(kind), wrap(err)
}

func (m meta) DDL(ctx context.Context, t db.TableRef) (string, error) {
	kind, err := m.kind(ctx, t)
	if err != nil {
		return "", err
	}
	if kind != "U" {
		var def sql.NullString
		if err := m.c.db.QueryRowContext(ctx, `SELECT OBJECT_DEFINITION(OBJECT_ID(@p1))`, objName(t)).Scan(&def); err != nil {
			return "", wrap(err)
		}
		if !def.Valid {
			return "", fmt.Errorf("%s: definition not visible: %w", objName(t), db.ErrNotFound)
		}
		return strings.TrimSpace(def.String), nil
	}
	s, err := m.TableSchema(ctx, t)
	if err != nil {
		return "", err
	}
	return buildTableDDL(s), nil
}

// TableSchema combines Columns, Indexes and ForeignKeys with the table's
// comment. Types and defaults are as SQL Server spells them, not
// normalized: the schema tools that need that are off for SQL Server.
func (m meta) TableSchema(ctx context.Context, t db.TableRef) (db.TableSchema, error) {
	s := db.TableSchema{Ref: t}
	var comment string
	err := m.c.db.QueryRowContext(ctx, `SELECT `+fmt.Sprintf(description, "o.object_id", "0")+`
		FROM sys.objects o WHERE o.object_id = OBJECT_ID(@p1) AND o.type IN ('U', 'V')`, objName(t)).Scan(&comment)
	if errors.Is(err, sql.ErrNoRows) {
		return s, fmt.Errorf("%s: %w", objName(t), db.ErrNotFound)
	}
	if err != nil {
		return s, wrap(err)
	}
	s.Comment = comment
	if s.Columns, err = m.Columns(ctx, t); err != nil {
		return s, err
	}
	for i := range s.Columns {
		s.Columns[i].Position = i + 1
	}
	if s.Indexes, err = m.Indexes(ctx, t); err != nil {
		return s, err
	}
	s.ForeignKeys, err = m.ForeignKeys(ctx, t)
	return s, err
}
