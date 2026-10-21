package oracle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
)

type meta struct{ c *conn }

// userOwners filters owners down to the users Oracle does not maintain.
const userOwners = `(SELECT username FROM all_users WHERE oracle_maintained = 'N')`

// owner is the schema of ns: the connected user when it names none.
func (m meta) owner(ns db.Namespace) string {
	if ns.Schema == "" {
		return m.c.user
	}
	return ns.Schema
}

func (m meta) strings(ctx context.Context, query string, args ...any) ([]string, error) {
	var out []string
	var s string
	err := m.each(ctx, query, args, []any{&s}, func() { out = append(out, s) })
	return out, err
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
	return wrap(rows.Err())
}

// Databases is empty: a connection sees one database, its service. The
// tree lists the schemas right under the connection.
func (meta) Databases(context.Context) ([]string, error) { return nil, nil }

// Schemas lists the users Oracle does not maintain, the connected one first.
func (m meta) Schemas(ctx context.Context, _ string) ([]string, error) {
	return m.strings(ctx, `SELECT username FROM all_users WHERE oracle_maintained = 'N'
		ORDER BY CASE WHEN username = USER THEN 0 ELSE 1 END, username`)
}

func (m meta) Tables(ctx context.Context, ns db.Namespace) ([]db.Table, error) {
	var out []db.Table
	var t db.Table
	var kind string
	var comment sql.NullString
	err := m.each(ctx, `SELECT o.object_name, o.object_type, c.comments
		FROM all_objects o
		LEFT JOIN all_tab_comments c ON c.owner = o.owner AND c.table_name = o.object_name
		WHERE o.owner = :1 AND o.object_type IN ('TABLE', 'VIEW') AND o.secondary = 'N'
		  AND o.object_name NOT LIKE 'BIN$%'
		ORDER BY o.object_name`,
		[]any{m.owner(ns)}, []any{&t.Name, &kind, &comment}, func() {
			t.Kind = db.TableKindTable
			if kind == "VIEW" {
				t.Kind = db.TableKindView
			}
			t.Comment = comment.String
			out = append(out, t)
		})
	return out, err
}

// typeName spells a column type the way Oracle's DDL does, in lower case.
func typeName(typ string, length, charLen sql.NullInt64, charUsed string, prec, scale sql.NullInt64) string {
	t := strings.ToLower(typ)
	switch t {
	case "varchar2", "nvarchar2", "char", "nchar":
		n := charLen.Int64
		if n == 0 {
			n = length.Int64
		}
		suffix := ""
		if charUsed == "C" && (t == "varchar2" || t == "char") {
			suffix = " char"
		}
		return t + "(" + strconv.FormatInt(n, 10) + suffix + ")"
	case "raw":
		return t + "(" + strconv.FormatInt(length.Int64, 10) + ")"
	case "number":
		switch {
		case !prec.Valid && !scale.Valid:
			return t
		case !prec.Valid:
			return fmt.Sprintf("number(*,%d)", scale.Int64)
		case scale.Int64 == 0:
			return fmt.Sprintf("number(%d)", prec.Int64)
		}
		return fmt.Sprintf("number(%d,%d)", prec.Int64, scale.Int64)
	case "float":
		if prec.Valid {
			return fmt.Sprintf("float(%d)", prec.Int64)
		}
	}
	return t
}

func (m meta) Columns(ctx context.Context, t db.TableRef) ([]db.Column, error) {
	var out []db.Column
	var c db.Column
	var typ, nullable, charUsed string
	var length, charLen, prec, scale sql.NullInt64
	var def, comment sql.NullString
	var pk int
	err := m.each(ctx, `SELECT c.column_name, c.data_type, c.data_length, c.char_length, NVL(c.char_used, 'B'),
		       c.data_precision, c.data_scale, c.nullable, c.data_default, cc.comments,
		       CASE WHEN EXISTS (SELECT 1 FROM all_constraints k
		            JOIN all_cons_columns kc ON kc.owner = k.owner AND kc.constraint_name = k.constraint_name
		            WHERE k.owner = c.owner AND k.table_name = c.table_name AND k.constraint_type = 'P'
		              AND kc.column_name = c.column_name) THEN 1 ELSE 0 END
		FROM all_tab_columns c
		LEFT JOIN all_col_comments cc ON cc.owner = c.owner AND cc.table_name = c.table_name AND cc.column_name = c.column_name
		WHERE c.owner = :1 AND c.table_name = :2
		ORDER BY c.column_id`,
		[]any{m.owner(t.Namespace), t.Name},
		[]any{&c.Name, &typ, &length, &charLen, &charUsed, &prec, &scale, &nullable, &def, &comment, &pk},
		func() {
			c.Type = typeName(typ, length, charLen, charUsed, prec, scale)
			c.Nullable = nullable == "Y"
			c.Comment = comment.String
			c.PrimaryKey = pk == 1
			c.Default = nil
			if d := strings.TrimSpace(def.String); def.Valid && d != "" && !strings.EqualFold(d, "NULL") {
				c.Default = &d
			}
			out = append(out, c)
		})
	return out, err
}

func (m meta) Indexes(ctx context.Context, t db.TableRef) ([]db.Index, error) {
	var out []db.Index
	var name, uniqueness, col string
	var primary int
	err := m.each(ctx, `SELECT i.index_name, i.uniqueness, ic.column_name,
		       CASE WHEN EXISTS (SELECT 1 FROM all_constraints k
		            WHERE k.owner = i.table_owner AND k.table_name = i.table_name
		              AND k.constraint_type = 'P' AND k.index_name = i.index_name) THEN 1 ELSE 0 END
		FROM all_indexes i
		JOIN all_ind_columns ic ON ic.index_owner = i.owner AND ic.index_name = i.index_name
		WHERE i.table_owner = :1 AND i.table_name = :2 AND i.index_type <> 'LOB'
		ORDER BY i.index_name, ic.column_position`,
		[]any{m.owner(t.Namespace), t.Name}, []any{&name, &uniqueness, &col, &primary}, func() {
			if n := len(out); n > 0 && out[n-1].Name == name {
				out[n-1].Columns = append(out[n-1].Columns, col)
				return
			}
			out = append(out, db.Index{Name: name, Unique: uniqueness == "UNIQUE", Primary: primary == 1, Columns: []string{col}})
		})
	return out, err
}

func (m meta) ForeignKeys(ctx context.Context, t db.TableRef) ([]db.ForeignKey, error) {
	var out []db.ForeignKey
	var name, col, refOwner, refTable, refCol string
	err := m.each(ctx, `SELECT c.constraint_name, cc.column_name, r.owner, r.table_name, rc.column_name
		FROM all_constraints c
		JOIN all_cons_columns cc ON cc.owner = c.owner AND cc.constraint_name = c.constraint_name
		JOIN all_constraints r ON r.owner = c.r_owner AND r.constraint_name = c.r_constraint_name
		JOIN all_cons_columns rc ON rc.owner = r.owner AND rc.constraint_name = r.constraint_name AND rc.position = cc.position
		WHERE c.owner = :1 AND c.table_name = :2 AND c.constraint_type = 'R'
		ORDER BY c.constraint_name, cc.position`,
		[]any{m.owner(t.Namespace), t.Name}, []any{&name, &col, &refOwner, &refTable, &refCol}, func() {
			if n := len(out); n > 0 && out[n-1].Name == name {
				out[n-1].Columns = append(out[n-1].Columns, col)
				out[n-1].RefColumns = append(out[n-1].RefColumns, refCol)
				return
			}
			out = append(out, db.ForeignKey{
				Name: name, Columns: []string{col}, RefColumns: []string{refCol},
				RefTable: db.TableRef{Namespace: db.Namespace{Database: t.Database, Schema: refOwner}, Name: refTable},
			})
		})
	return out, err
}

// objectType is the all_objects type of name in owner among types, or
// ErrNotFound.
func (m meta) objectType(ctx context.Context, owner, name string, types ...string) (string, error) {
	in := make([]string, len(types))
	args := []any{owner, name}
	for i, t := range types {
		in[i] = ":" + strconv.Itoa(i+3)
		args = append(args, t)
	}
	var kind string
	err := m.c.db.QueryRowContext(ctx, `SELECT object_type FROM all_objects
		WHERE owner = :1 AND object_name = :2 AND object_type IN (`+strings.Join(in, ", ")+`)
		ORDER BY object_type`, args...).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%s.%s: %w", owner, name, db.ErrNotFound)
	}
	return kind, wrap(err)
}

// getDDL reads DBMS_METADATA's DDL without storage and segment clauses.
// The transform settings belong to the session, so both run on one
// connection.
func (m meta) getDDL(ctx context.Context, kind, owner, name string) (string, error) {
	sc, err := m.c.db.Conn(ctx)
	if err != nil {
		return "", wrap(err)
	}
	defer func() { _ = sc.Close() }()
	if _, err := sc.ExecContext(ctx, `BEGIN
		DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'STORAGE', FALSE);
		DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SEGMENT_ATTRIBUTES', FALSE);
		DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SQLTERMINATOR', TRUE);
	END;`); err != nil {
		return "", wrap(err)
	}
	var ddl string
	if err := sc.QueryRowContext(ctx, `SELECT DBMS_METADATA.GET_DDL(:1, :2, :3) FROM dual`,
		strings.ReplaceAll(kind, " ", "_"), name, owner).Scan(&ddl); err != nil {
		return "", wrap(err)
	}
	return strings.TrimSpace(ddl), nil
}

func (m meta) DDL(ctx context.Context, t db.TableRef) (string, error) {
	owner := m.owner(t.Namespace)
	kind, err := m.objectType(ctx, owner, t.Name, "TABLE", "VIEW")
	if err != nil {
		return "", err
	}
	return m.getDDL(ctx, kind, owner, t.Name)
}

// TableSchema combines Columns, Indexes and ForeignKeys with the table's
// comment. Types and defaults are as Oracle spells them, not normalized:
// the schema tools that need that are off for Oracle.
func (m meta) TableSchema(ctx context.Context, t db.TableRef) (db.TableSchema, error) {
	s := db.TableSchema{Ref: t}
	owner := m.owner(t.Namespace)
	if _, err := m.objectType(ctx, owner, t.Name, "TABLE", "VIEW"); err != nil {
		return s, err
	}
	var comment sql.NullString
	err := m.c.db.QueryRowContext(ctx, `SELECT comments FROM all_tab_comments WHERE owner = :1 AND table_name = :2`,
		owner, t.Name).Scan(&comment)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return s, wrap(err)
	}
	s.Comment = comment.String
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
