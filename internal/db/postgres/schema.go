package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/shepherd-yun/datavim/internal/db"
)

// serialDefault is the default a serial column gets from its sequence.
var serialDefault = regexp.MustCompile(`^nextval\('.*'::regclass\)$`)

// TableSchema reads t for schema comparison: what Columns, Indexes and
// ForeignKeys return, plus what only comparison needs, normalized.
func (m meta) TableSchema(ctx context.Context, t db.TableRef) (db.TableSchema, error) {
	s := db.TableSchema{Ref: t}
	if err := m.checkDatabase(t.Database); err != nil {
		return s, err
	}
	name := regclass(t)
	err := m.c.pool.QueryRow(ctx, `
		SELECT COALESCE(obj_description(c.oid, 'pg_class'), '') FROM pg_class c
		WHERE c.oid = to_regclass($1)`, name).Scan(&s.Comment)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, fmt.Errorf("%s: %w", name, db.ErrNotFound)
	}
	if err != nil {
		return s, err
	}
	if s.Columns, err = m.Columns(ctx, t); err != nil {
		return s, err
	}
	if err := m.columnDetails(ctx, name, s.Columns); err != nil {
		return s, err
	}
	if s.Indexes, err = m.Indexes(ctx, t); err != nil {
		return s, err
	}
	if err := m.indexDetails(ctx, name, s.Indexes); err != nil {
		return s, err
	}
	s.ForeignKeys, err = m.ForeignKeys(ctx, t)
	return s, err
}

// columnDetails fills in what Columns leaves out and normalizes types and
// defaults. name is the table's regclass text.
func (m meta) columnDetails(ctx context.Context, name string, cols []db.Column) error {
	rows, err := m.c.pool.Query(ctx, `
		SELECT a.attname::text, a.attidentity::text, a.attgenerated::text,
		       CASE WHEN a.attcollation <> 0 AND a.attcollation <> ty.typcollation
		            THEN co.collname::text ELSE '' END
		FROM pg_attribute a
		JOIN pg_type ty ON ty.oid = a.atttypid
		LEFT JOIN pg_collation co ON co.oid = a.attcollation
		WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped`, name)
	if err != nil {
		return err
	}
	type detail struct{ identity, generated, collation string }
	details := map[string]detail{}
	var col string
	var d detail
	if _, err := pgx.ForEachRow(rows, []any{&col, &d.identity, &d.generated, &d.collation}, func() error {
		details[col] = d
		return nil
	}); err != nil {
		return err
	}
	for i := range cols {
		c := &cols[i]
		d := details[c.Name]
		c.Position = i + 1
		c.Type = normType(c.Type)
		c.Collation = d.collation
		switch {
		case d.generated == "s": // the generation expression is stored as the default
			if c.Default != nil {
				c.Generated = *c.Default
			}
			c.Default = nil
		case d.identity != "":
			c.AutoIncrement = true
		case c.Default != nil && serialDefault.MatchString(*c.Default):
			c.AutoIncrement, c.Default = true, nil
		}
		c.Default = normDefault(c.Default)
	}
	return nil
}

// indexDetails fills in Method, Where and Constraint.
func (m meta) indexDetails(ctx context.Context, name string, idx []db.Index) error {
	rows, err := m.c.pool.Query(ctx, `
		SELECT i.relname::text, am.amname::text, COALESCE(pg_get_expr(ix.indpred, ix.indrelid, true), ''),
		       EXISTS (SELECT 1 FROM pg_constraint con
		               WHERE con.conrelid = ix.indrelid AND con.conindid = ix.indexrelid
		                 AND con.contype IN ('p', 'u'))
		FROM pg_index ix
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_am am ON am.oid = i.relam
		WHERE ix.indrelid = to_regclass($1)`, name)
	if err != nil {
		return err
	}
	type detail struct {
		method, where string
		constraint    bool
	}
	details := map[string]detail{}
	var ixName string
	var d detail
	if _, err := pgx.ForEachRow(rows, []any{&ixName, &d.method, &d.where, &d.constraint}, func() error {
		details[ixName] = d
		return nil
	}); err != nil {
		return err
	}
	for i := range idx {
		d := details[idx[i].Name]
		idx[i].Method, idx[i].Where, idx[i].Constraint = d.method, d.where, d.constraint
	}
	return nil
}
