package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
)

func (m meta) Catalog(ctx context.Context) (db.Catalog, error) {
	var c db.Catalog
	var err error
	if c.Databases, err = m.Databases(ctx); err != nil {
		return c, err
	}
	database, err := m.current(ctx)
	if err != nil {
		return c, err
	}
	ns := func(schema string) db.Namespace { return db.Namespace{Database: database, Schema: schema} }
	schemas, err := m.Schemas(ctx, "")
	if err != nil {
		return c, err
	}
	for _, s := range schemas {
		c.Namespaces = append(c.Namespaces, ns(s))
	}

	var schema, name, kind string
	err = m.each(ctx, `SELECT s.name, o.name, o.type FROM sys.objects o
		JOIN sys.schemas s ON s.schema_id = o.schema_id
		WHERE `+userSchemas+` AND o.type IN ('U', 'V') AND o.is_ms_shipped = 0 ORDER BY 1, 2`,
		nil, []any{&schema, &name, &kind}, func() {
			t := db.CatalogTable{Namespace: ns(schema), Name: name}
			if strings.TrimSpace(kind) == "V" {
				t.Kind = db.TableKindView
			}
			c.Tables = append(c.Tables, t)
		})
	if err != nil {
		return c, err
	}

	var col, typ string
	var maxLen, prec, scale int
	err = m.each(ctx, `SELECT s.name, o.name, c.name, ty.name, c.max_length, c.precision, c.scale
		FROM sys.columns c
		JOIN sys.objects o ON o.object_id = c.object_id
		JOIN sys.schemas s ON s.schema_id = o.schema_id
		JOIN sys.types ty ON ty.user_type_id = c.user_type_id
		WHERE `+userSchemas+` AND o.type IN ('U', 'V') AND o.is_ms_shipped = 0
		ORDER BY s.name, o.name, c.column_id`,
		nil, []any{&schema, &name, &col, &typ, &maxLen, &prec, &scale}, func() {
			c.Columns = append(c.Columns, db.CatalogColumn{
				Table: db.TableRef{Namespace: ns(schema), Name: name}, Name: col, Type: typeName(typ, maxLen, prec, scale),
			})
		})
	if err != nil {
		return c, err
	}

	err = m.each(ctx, `SELECT s.name, o.name, o.type FROM sys.objects o
		JOIN sys.schemas s ON s.schema_id = o.schema_id
		WHERE `+userSchemas+` AND o.type IN ('P', 'FN', 'IF', 'TF') AND o.is_ms_shipped = 0 ORDER BY 1, 2`,
		nil, []any{&schema, &name, &kind}, func() {
			r := db.Routine{Namespace: ns(schema), Name: name}
			if strings.TrimSpace(kind) == "P" {
				r.Kind = db.RoutineProcedure
			}
			c.Routines = append(c.Routines, r)
		})
	return c, err
}

func (m meta) RoutineDDL(ctx context.Context, r db.Routine) (string, error) {
	if err := m.checkDatabase(ctx, r.Namespace.Database); err != nil {
		return "", err
	}
	name := objName(db.TableRef{Namespace: r.Namespace, Name: r.Name})
	var def sql.NullString
	err := m.c.db.QueryRowContext(ctx, `SELECT OBJECT_DEFINITION(OBJECT_ID(@p1))`, name).Scan(&def)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !def.Valid {
		return "", fmt.Errorf("%s: %w", name, db.ErrNotFound)
	}
	return strings.TrimSpace(def.String), wrap(err)
}
