package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/shepherd-yun/datavim/internal/db"
)

type meta struct{ c *conn }

func (m meta) strings(ctx context.Context, sql string, args ...any) ([]string, error) {
	rows, err := m.c.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (m meta) checkDatabase(database string) error {
	if database != "" && database != m.c.database {
		return fmt.Errorf("postgres: connection is bound to database %q, not %q", m.c.database, database)
	}
	return nil
}

func regclass(t db.TableRef) string { return dialect{}.QualifiedName(t) }

func (m meta) Databases(ctx context.Context) ([]string, error) {
	return m.strings(ctx, `SELECT datname::text FROM pg_database
		WHERE NOT datistemplate AND datallowconn ORDER BY 1`)
}

func (m meta) Schemas(ctx context.Context, database string) ([]string, error) {
	if err := m.checkDatabase(database); err != nil {
		return nil, err
	}
	return m.strings(ctx, `SELECT nspname::text FROM pg_namespace
		WHERE nspname !~ '^pg_' AND nspname <> 'information_schema' ORDER BY 1`)
}

func (m meta) Tables(ctx context.Context, ns db.Namespace) ([]db.Table, error) {
	if err := m.checkDatabase(ns.Database); err != nil {
		return nil, err
	}
	rows, err := m.c.pool.Query(ctx, `
		SELECT c.relname::text, c.relkind::text, COALESCE(obj_description(c.oid, 'pg_class'), '')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r','p','v','m','f') AND NOT c.relispartition
		ORDER BY 1`, ns.Schema)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (db.Table, error) {
		var t db.Table
		var kind string
		err := row.Scan(&t.Name, &kind, &t.Comment)
		if kind == "v" || kind == "m" {
			t.Kind = db.TableKindView
		}
		return t, err
	})
}

func (m meta) Columns(ctx context.Context, t db.TableRef) ([]db.Column, error) {
	rows, err := m.c.pool.Query(ctx, `
		SELECT a.attname::text,
		       format_type(a.atttypid, a.atttypmod),
		       NOT a.attnotnull,
		       pg_get_expr(d.adbin, d.adrelid),
		       COALESCE(col_description(a.attrelid, a.attnum), ''),
		       EXISTS (SELECT 1 FROM pg_index i
		               WHERE i.indrelid = a.attrelid AND i.indisprimary AND a.attnum = ANY(i.indkey))
		FROM pg_attribute a
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, regclass(t))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (db.Column, error) {
		var c db.Column
		err := row.Scan(&c.Name, &c.Type, &c.Nullable, &c.Default, &c.Comment, &c.PrimaryKey)
		return c, err
	})
}

func (m meta) Indexes(ctx context.Context, t db.TableRef) ([]db.Index, error) {
	rows, err := m.c.pool.Query(ctx, `
		SELECT i.relname::text, ix.indisunique, ix.indisprimary,
		       ARRAY(SELECT pg_get_indexdef(ix.indexrelid, k, true)
		             FROM generate_series(1, ix.indnkeyatts) AS k ORDER BY k)
		FROM pg_index ix JOIN pg_class i ON i.oid = ix.indexrelid
		WHERE ix.indrelid = to_regclass($1)
		ORDER BY 1`, regclass(t))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (db.Index, error) {
		var i db.Index
		err := row.Scan(&i.Name, &i.Unique, &i.Primary, &i.Columns)
		return i, err
	})
}

func (m meta) ForeignKeys(ctx context.Context, t db.TableRef) ([]db.ForeignKey, error) {
	rows, err := m.c.pool.Query(ctx, `
		SELECT con.conname::text,
		       ARRAY(SELECT a.attname::text FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
		             JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum ORDER BY k.ord),
		       rn.nspname::text, rc.relname::text,
		       ARRAY(SELECT a.attname::text FROM unnest(con.confkey) WITH ORDINALITY AS k(attnum, ord)
		             JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum ORDER BY k.ord)
		FROM pg_constraint con
		JOIN pg_class rc ON rc.oid = con.confrelid
		JOIN pg_namespace rn ON rn.oid = rc.relnamespace
		WHERE con.contype = 'f' AND con.conrelid = to_regclass($1)
		ORDER BY 1`, regclass(t))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (db.ForeignKey, error) {
		var fk db.ForeignKey
		var schema, table string
		err := row.Scan(&fk.Name, &fk.Columns, &schema, &table, &fk.RefColumns)
		fk.RefTable = db.TableRef{Namespace: db.Namespace{Database: m.c.database, Schema: schema}, Name: table}
		return fk, err
	})
}

func (m meta) DDL(ctx context.Context, t db.TableRef) (string, error) {
	name := regclass(t)
	var kind string
	err := m.c.pool.QueryRow(ctx, `SELECT relkind::text FROM pg_class WHERE oid = to_regclass($1)`, name).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%s: %w", name, db.ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	if kind == "v" || kind == "m" {
		var def string
		if err := m.c.pool.QueryRow(ctx, `SELECT pg_get_viewdef(to_regclass($1), true)`, name).Scan(&def); err != nil {
			return "", err
		}
		keyword := "VIEW"
		if kind == "m" {
			keyword = "MATERIALIZED VIEW"
		}
		return fmt.Sprintf("CREATE %s %s AS\n%s", keyword, name, def), nil
	}

	cols, err := m.Columns(ctx, t)
	if err != nil {
		return "", err
	}
	rows, err := m.c.pool.Query(ctx, `
		SELECT conname::text, pg_get_constraintdef(oid, true) FROM pg_constraint
		WHERE conrelid = to_regclass($1) AND contype IN ('p','u','f','c','x')
		ORDER BY contype <> 'p', conname`, name)
	if err != nil {
		return "", err
	}
	cons, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (constraint, error) {
		var c constraint
		err := row.Scan(&c.name, &c.def)
		return c, err
	})
	if err != nil {
		return "", err
	}
	// Indexes that back a constraint are already covered by the constraint.
	indexDefs, err := m.strings(ctx, `
		SELECT pg_get_indexdef(indexrelid) FROM pg_index
		WHERE indrelid = to_regclass($1)
		  AND indexrelid NOT IN (SELECT conindid FROM pg_constraint WHERE conrelid = to_regclass($1))
		ORDER BY 1`, name)
	if err != nil {
		return "", err
	}
	return buildTableDDL(name, cols, cons, indexDefs), nil
}
