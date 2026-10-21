package oracle

import (
	"context"
	"database/sql"

	"github.com/shepherd-yun/datavim/internal/db"
)

func (m meta) Catalog(ctx context.Context) (db.Catalog, error) {
	var c db.Catalog
	ns := func(owner string) db.Namespace { return db.Namespace{Schema: owner} }
	schemas, err := m.Schemas(ctx, "")
	if err != nil {
		return c, err
	}
	for _, s := range schemas {
		c.Namespaces = append(c.Namespaces, ns(s))
	}

	var owner, name, kind string
	err = m.each(ctx, `SELECT owner, object_name, object_type FROM all_objects
		WHERE owner IN `+userOwners+` AND object_type IN ('TABLE', 'VIEW') AND secondary = 'N'
		  AND object_name NOT LIKE 'BIN$%'
		ORDER BY 1, 2`, nil, []any{&owner, &name, &kind}, func() {
		t := db.CatalogTable{Namespace: ns(owner), Name: name}
		if kind == "VIEW" {
			t.Kind = db.TableKindView
		}
		c.Tables = append(c.Tables, t)
	})
	if err != nil {
		return c, err
	}

	var col, typ, charUsed string
	var length, charLen, prec, scale sql.NullInt64
	err = m.each(ctx, `SELECT owner, table_name, column_name, data_type, data_length, char_length,
		       NVL(char_used, 'B'), data_precision, data_scale
		FROM all_tab_columns
		WHERE owner IN `+userOwners+` AND table_name NOT LIKE 'BIN$%'
		ORDER BY owner, table_name, column_id`, nil,
		[]any{&owner, &name, &col, &typ, &length, &charLen, &charUsed, &prec, &scale}, func() {
			c.Columns = append(c.Columns, db.CatalogColumn{
				Table: db.TableRef{Namespace: ns(owner), Name: name}, Name: col,
				Type: typeName(typ, length, charLen, charUsed, prec, scale),
			})
		})
	if err != nil {
		return c, err
	}

	err = m.each(ctx, `SELECT owner, object_name, object_type FROM all_objects
		WHERE owner IN `+userOwners+` AND object_type IN ('FUNCTION', 'PROCEDURE', 'PACKAGE')
		ORDER BY 1, 2`, nil, []any{&owner, &name, &kind}, func() {
		r := db.Routine{Namespace: ns(owner), Name: name}
		if kind != "FUNCTION" {
			r.Kind = db.RoutineProcedure // packages too: their procedures run with EXEC/CALL
		}
		c.Routines = append(c.Routines, r)
	})
	return c, err
}

func (m meta) RoutineDDL(ctx context.Context, r db.Routine) (string, error) {
	owner := m.owner(r.Namespace)
	kind, err := m.objectType(ctx, owner, r.Name, "FUNCTION", "PROCEDURE", "PACKAGE")
	if err != nil {
		return "", err
	}
	return m.getDDL(ctx, kind, owner, r.Name)
}
