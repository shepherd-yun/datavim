package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// userSchemas filters pg_namespace n down to the schemas Schemas lists.
const userSchemas = `n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'`

func (m meta) ns(schema string) db.Namespace {
	return db.Namespace{Database: m.c.database, Schema: schema}
}

func (m meta) Catalog(ctx context.Context) (db.Catalog, error) {
	var c db.Catalog
	var err error
	if c.Databases, err = m.Databases(ctx); err != nil {
		return c, err
	}
	schemas, err := m.Schemas(ctx, "")
	if err != nil {
		return c, err
	}
	for _, s := range schemas {
		c.Namespaces = append(c.Namespaces, m.ns(s))
	}

	rows, err := m.c.pool.Query(ctx, `
		SELECT n.nspname::text, c.relname::text, c.relkind::text
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE `+userSchemas+` AND c.relkind IN ('r','p','v','m','f') AND NOT c.relispartition
		ORDER BY 1, 2`)
	if err != nil {
		return c, err
	}
	c.Tables, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (db.CatalogTable, error) {
		var t db.CatalogTable
		var schema, kind string
		err := row.Scan(&schema, &t.Name, &kind)
		t.Namespace = m.ns(schema)
		if kind == "v" || kind == "m" {
			t.Kind = db.TableKindView
		}
		return t, err
	})
	if err != nil {
		return c, err
	}

	rows, err = m.c.pool.Query(ctx, `
		SELECT n.nspname::text, c.relname::text, a.attname::text, format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE `+userSchemas+` AND c.relkind IN ('r','p','v','m','f') AND NOT c.relispartition
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY n.nspname, c.relname, a.attnum`)
	if err != nil {
		return c, err
	}
	c.Columns, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (db.CatalogColumn, error) {
		var col db.CatalogColumn
		var schema string
		err := row.Scan(&schema, &col.Table.Name, &col.Name, &col.Type)
		col.Table.Namespace = m.ns(schema)
		return col, err
	})
	if err != nil {
		return c, err
	}

	rows, err = m.c.pool.Query(ctx, `
		SELECT n.nspname::text, p.proname::text, p.prokind::text, pg_get_function_identity_arguments(p.oid)
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE `+userSchemas+` AND p.prokind IN ('f','p')
		ORDER BY 1, 2, 4`)
	if err != nil {
		return c, err
	}
	c.Routines, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (db.Routine, error) {
		var r db.Routine
		var schema, kind string
		err := row.Scan(&schema, &r.Name, &kind, &r.Args)
		r.Namespace = m.ns(schema)
		if kind == "p" {
			r.Kind = db.RoutineProcedure
		}
		return r, err
	})
	return c, err
}

func (m meta) RoutineDDL(ctx context.Context, r db.Routine) (string, error) {
	if err := m.checkDatabase(r.Namespace.Database); err != nil {
		return "", err
	}
	var def string
	err := m.c.pool.QueryRow(ctx, `
		SELECT pg_get_functiondef(p.oid)
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = $1 AND p.proname = $2 AND pg_get_function_identity_arguments(p.oid) = $3`,
		r.Namespace.Schema, r.Name, r.Args).Scan(&def)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%s(%s): %w", r.Name, r.Args, db.ErrNotFound)
	}
	return def, err
}
