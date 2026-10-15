package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// TableSchema reads t for schema comparison: what Columns, Indexes and
// ForeignKeys return, plus what only comparison needs, normalized.
func (m meta) TableSchema(ctx context.Context, t db.TableRef) (db.TableSchema, error) {
	s := db.TableSchema{Ref: t, Options: map[string]string{}}
	var engine, collation, comment sql.NullString
	err := m.c.db.QueryRowContext(ctx, `
		SELECT ENGINE, TABLE_COLLATION, TABLE_COMMENT FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, t.Database, t.Name).Scan(&engine, &collation, &comment)
	if errors.Is(err, sql.ErrNoRows) {
		return s, fmt.Errorf("%s: %w", dialect{}.QualifiedName(t), db.ErrNotFound)
	}
	if err != nil {
		return s, err
	}
	s.Comment = comment.String
	if engine.Valid {
		s.Options["engine"] = engine.String
	}
	if collation.Valid {
		s.Options["collation"] = collation.String
	}
	if s.Columns, err = m.Columns(ctx, t); err != nil {
		return s, err
	}
	if err := m.columnDetails(ctx, &s); err != nil {
		return s, err
	}
	if s.Indexes, err = m.Indexes(ctx, t); err != nil {
		return s, err
	}
	if err := m.indexMethods(ctx, t, s.Indexes); err != nil {
		return s, err
	}
	s.ForeignKeys, err = m.ForeignKeys(ctx, t)
	return s, err
}

// columnDetails fills in what Columns leaves out and normalizes types and
// defaults.
func (m meta) columnDetails(ctx context.Context, s *db.TableSchema) error {
	rows, err := m.c.db.QueryContext(ctx, `
		SELECT COLUMN_NAME, EXTRA, COALESCE(COLLATION_NAME, ''), COALESCE(GENERATION_EXPRESSION, '')
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, s.Ref.Database, s.Ref.Name)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type detail struct{ extra, collation, generated string }
	details := map[string]detail{}
	for rows.Next() {
		var name string
		var d detail
		if err := rows.Scan(&name, &d.extra, &d.collation, &d.generated); err != nil {
			return err
		}
		d.extra = strings.ToLower(d.extra)
		details[name] = d
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range s.Columns {
		c := &s.Columns[i]
		d := details[c.Name]
		c.Position = i + 1
		c.Type = normType(c.Type)
		c.AutoIncrement = strings.Contains(d.extra, "auto_increment")
		// "virtual generated" / "stored generated"; "default_generated"
		// marks an expression default instead.
		if strings.HasSuffix(d.extra, "generated") && !strings.Contains(d.extra, "default_generated") {
			c.Generated, c.Default = d.generated, nil
		}
		if d.collation != "" && d.collation != s.Options["collation"] {
			c.Collation = d.collation
		}
		c.Default = normDefault(c.Default, c.Type, d.extra)
	}
	return nil
}

// indexMethods fills in Method (btree, fulltext, spatial, hash).
func (m meta) indexMethods(ctx context.Context, t db.TableRef, idx []db.Index) error {
	rows, err := m.c.db.QueryContext(ctx, `
		SELECT DISTINCT INDEX_NAME, INDEX_TYPE FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, t.Database, t.Name)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	methods := map[string]string{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return err
		}
		methods[name] = strings.ToLower(typ)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range idx {
		idx[i].Method = methods[idx[i].Name]
	}
	return nil
}
