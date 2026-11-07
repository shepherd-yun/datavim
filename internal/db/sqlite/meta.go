package sqlite

import (
	"context"
	"slices"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// tableOptions is the key of TableSchema.Options for what follows the columns
// in CREATE TABLE. It is always set, so that schema compare notices a table
// that lacks them too.
const tableOptions = "table options"

// primaryName names a primary key that has no name: a rowid alias has no
// index at all, and an unnamed constraint's index is SQLite's business.
const primaryName = "primary"

// userObjects selects what the tree lists; sqlite_master exists on every
// version, sqlite_schema only since 3.33.
const userObjects = `type IN ('table', 'view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\'`

type meta struct{ c *conn }

func (meta) Databases(context.Context) ([]string, error) { return nil, nil }

func (meta) Schemas(context.Context, string) ([]string, error) { return nil, nil }

func (m meta) Tables(ctx context.Context, _ db.Namespace) ([]db.Table, error) {
	rs, err := m.c.Query(ctx, "SELECT name, type FROM sqlite_master WHERE "+userObjects+" ORDER BY name")
	if err != nil {
		return nil, err
	}
	out := make([]db.Table, 0, len(rs.Rows))
	for _, r := range rs.Rows {
		out = append(out, db.Table{Name: r[0].Text, Kind: kindOf(r[1].Text)})
	}
	return out, nil
}

func kindOf(t string) db.TableKind {
	if t == "view" {
		return db.TableKindView
	}
	return db.TableKindTable
}

// Columns lists the columns: a virtual table's hidden ones are left out, the
// generated ones kept. A rowid alias counts as auto-increment, since NULL
// numbers it (the importer relies on that).
func (m meta) Columns(ctx context.Context, t db.TableRef) ([]db.Column, error) {
	cols, err := m.xinfo(ctx, t.Name)
	if err != nil {
		return nil, err
	}
	for i, c := range cols {
		if c.PrimaryKey && strings.EqualFold(c.Type, "INTEGER") && pkColumns(cols) == 1 {
			alias, err := m.pkHasNoIndex(ctx, t.Name)
			if err != nil {
				return nil, err
			}
			cols[i].AutoIncrement = alias
		}
	}
	return cols, nil
}

// pkHasNoIndex tells a rowid alias from an INTEGER PRIMARY KEY that is not one
// (WITHOUT ROWID, or declared DESC): only those have an index of their own.
func (m meta) pkHasNoIndex(ctx context.Context, table string) (bool, error) {
	rs, err := m.c.Query(ctx, "SELECT 1 FROM pragma_index_list(?) WHERE origin = 'pk'", table)
	if err != nil {
		return false, err
	}
	return len(rs.Rows) == 0, nil
}

func pkColumns(cols []db.Column) int {
	n := 0
	for _, c := range cols {
		if c.PrimaryKey {
			n++
		}
	}
	return n
}

func (m meta) xinfo(ctx context.Context, table string) ([]db.Column, error) {
	rs, err := m.c.Query(ctx, `SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_xinfo(?) WHERE hidden <> 1 ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	out := make([]db.Column, 0, len(rs.Rows))
	for i, r := range rs.Rows {
		c := db.Column{
			Name: r[0].Text, Type: r[1].Text, PrimaryKey: r[4].Text != "0", Position: i + 1,
		}
		if c.Type == "" {
			c.Type = anyType
		}
		c.Nullable = r[2].Text == "0" && !c.PrimaryKey
		if !r[3].Null {
			d := r[3].Text
			c.Default = &d
		}
		out = append(out, c)
	}
	return out, nil
}

// primaryColumns are the primary key's columns in key order.
func (m meta) primaryColumns(ctx context.Context, table string) ([]string, error) {
	rs, err := m.c.Query(ctx, "SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk", table)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rs.Rows))
	for _, r := range rs.Rows {
		out = append(out, r[0].Text)
	}
	return out, nil
}

// Indexes lists the indexes; a primary key is always one of them, even a
// rowid alias's, which has no index in SQLite.
func (m meta) Indexes(ctx context.Context, t db.TableRef) ([]db.Index, error) {
	pk, err := m.primaryColumns(ctx, t.Name)
	if err != nil {
		return nil, err
	}
	var out []db.Index
	if len(pk) > 0 {
		out = append(out, db.Index{Name: primaryName, Columns: pk, Unique: true, Primary: true, Method: "btree", ParenColumns: parenNames(pk)})
	}
	rs, err := m.c.Query(ctx, `SELECT l.name, l."unique", l.origin, s.sql FROM pragma_index_list(?) l
		LEFT JOIN sqlite_master s ON s.type = 'index' AND s.name = l.name ORDER BY l.name`, t.Name)
	if err != nil {
		return nil, err
	}
	for _, r := range rs.Rows {
		if r[2].Text == "pk" { // a rowid alias has no such index, and no options
			if out[0].Suffixes, err = m.keySuffixes(ctx, t.Name, r[0].Text); err != nil {
				return nil, err
			}
			continue
		}
		ix := db.Index{Name: r[0].Text, Unique: r[1].Text != "0", Method: "btree", Constraint: r[2].Text == "u"}
		var entries, suffixes []string
		if !r[3].Null {
			entries, suffixes, ix.Where = sqlx.ParseSQLiteIndex(r[3].Text)
		}
		cols, err := m.c.Query(ctx, "SELECT name FROM pragma_index_info(?) ORDER BY seqno", ix.Name)
		if err != nil {
			return nil, err
		}
		for i, c := range cols.Rows {
			switch {
			case !c[0].Null:
				ix.Columns = append(ix.Columns, c[0].Text)
				ix.ParenColumns = append(ix.ParenColumns, parenNames([]string{c[0].Text})...)
			case i < len(entries):
				ix.Columns = append(ix.Columns, expressionEntry(entries[i]))
			}
		}
		switch {
		case r[3].Null: // a constraint has no CREATE INDEX to read them from
			if ix.Suffixes, err = m.keySuffixes(ctx, t.Name, ix.Name); err != nil {
				return nil, err
			}
		case len(suffixes) == len(ix.Columns) && strings.Join(suffixes, "") != "":
			ix.Suffixes = suffixes
		}
		out = append(out, ix)
	}
	return out, nil
}

// parenNames are the column names among names that would read as expressions.
func parenNames(names []string) []string {
	var out []string
	for _, n := range names {
		if strings.ContainsRune(n, '(') {
			out = append(out, n)
		}
	}
	return out
}

// keySuffixes rebuilds what follows each key of an index that SQLite made for a
// constraint; nil when nothing does. A collation only counts if it differs from
// the one the column declares, which the index inherits.
func (m meta) keySuffixes(ctx context.Context, table, index string) ([]string, error) {
	rs, err := m.c.Query(ctx, `SELECT name, "desc", coll FROM pragma_index_xinfo(?) WHERE key = 1 ORDER BY seqno`, index)
	if err != nil {
		return nil, err
	}
	text, err := m.tableSQL(ctx, table)
	if err != nil {
		return nil, err
	}
	declared := sqlx.ParseSQLiteTable(text).Columns
	out := make([]string, len(rs.Rows))
	found := false
	for i, r := range rs.Rows {
		collation := declared[strings.ToLower(r[0].Text)].Collation
		if collation == "" {
			collation = "BINARY"
		}
		var parts []string
		if !strings.EqualFold(r[2].Text, collation) {
			parts = append(parts, "COLLATE "+r[2].Text)
		}
		if r[1].Text != "0" {
			parts = append(parts, "DESC")
		}
		out[i] = strings.Join(parts, " ")
		found = found || out[i] != ""
	}
	if !found {
		return nil, nil
	}
	return out, nil
}

// expressionEntry spells an index expression so that it reads as one: an entry
// with a parenthesis is an expression everywhere else, and a+b has none.
func expressionEntry(e string) string {
	if strings.ContainsRune(e, '(') {
		return e
	}
	return "(" + e + ")"
}

// ForeignKeys reads the keys in the order they were declared. The pragma has
// no names: they come from the CREATE TABLE, or are made up for unnamed keys.
func (m meta) ForeignKeys(ctx context.Context, t db.TableRef) ([]db.ForeignKey, error) {
	rs, err := m.c.Query(ctx, `SELECT id, "table", "from", "to" FROM pragma_foreign_key_list(?) ORDER BY id DESC, seq`, t.Name)
	if err != nil {
		return nil, err
	}
	if len(rs.Rows) == 0 {
		return nil, nil
	}
	sqlText, err := m.tableSQL(ctx, t.Name)
	if err != nil {
		return nil, err
	}
	names := sqlx.ParseSQLiteTable(sqlText).ForeignKeys
	var out []db.ForeignKey
	id := ""
	for _, r := range rs.Rows {
		if r[0].Text != id {
			id = r[0].Text
			out = append(out, db.ForeignKey{RefTable: db.TableRef{Name: r[1].Text}})
		}
		fk := &out[len(out)-1]
		fk.Columns = append(fk.Columns, r[2].Text)
		if !r[3].Null {
			fk.RefColumns = append(fk.RefColumns, r[3].Text)
		}
	}
	for i := range out {
		fk := &out[i]
		key := strings.ToLower(strings.Join(fk.Columns, ","))
		fk.Name = names[key]
		if fk.Name == "" {
			fk.Name = "fk_" + t.Name + "_" + strings.Join(fk.Columns, "_")
		}
		if len(fk.RefColumns) == 0 { // REFERENCES parent: its primary key
			if fk.RefColumns, err = m.primaryColumns(ctx, fk.RefTable.Name); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// tableSQL is the object's CREATE statement, or ErrNotFound.
func (m meta) tableSQL(ctx context.Context, name string) (string, error) {
	rs, err := m.c.Query(ctx, "SELECT sql FROM sqlite_master WHERE type IN ('table', 'view') AND name = ? COLLATE NOCASE", name)
	if err != nil {
		return "", err
	}
	if len(rs.Rows) == 0 || rs.Rows[0][0].Null {
		return "", db.ErrNotFound
	}
	return rs.Rows[0][0].Text, nil
}

func (m meta) DDL(ctx context.Context, t db.TableRef) (string, error) { return m.tableSQL(ctx, t.Name) }

func (m meta) TableSchema(ctx context.Context, t db.TableRef) (db.TableSchema, error) {
	text, err := m.tableSQL(ctx, t.Name)
	if err != nil {
		return db.TableSchema{}, err
	}
	s := db.TableSchema{Ref: t}
	if s.Columns, err = m.xinfo(ctx, t.Name); err != nil {
		return db.TableSchema{}, err
	}
	parsed := sqlx.ParseSQLiteTable(text)
	s.Options = map[string]string{tableOptions: parsed.Options}
	for i := range s.Columns {
		c := &s.Columns[i]
		c.Type = normalizeType(strings.ToLower(c.Type))
		extra := parsed.Columns[strings.ToLower(c.Name)]
		c.Collation, c.Generated, c.AutoIncrement = extra.Collation, extra.Generated, extra.AutoIncrement
		if c.AutoIncrement || c.Generated != "" {
			c.Default = nil
		}
	}
	if s.Indexes, err = m.Indexes(ctx, t); err != nil {
		return db.TableSchema{}, err
	}
	if i := slices.IndexFunc(s.Indexes, func(ix db.Index) bool { return ix.Primary }); i >= 0 && parsed.PrimaryName != "" {
		s.Indexes[i].Name = parsed.PrimaryName
	}
	if s.ForeignKeys, err = m.ForeignKeys(ctx, t); err != nil {
		return db.TableSchema{}, err
	}
	return s, nil
}

// Catalog reads everything at once. A file has one namespace, the empty one.
func (m meta) Catalog(ctx context.Context) (db.Catalog, error) {
	c := db.Catalog{Namespaces: []db.Namespace{{}}}
	tables, err := m.Tables(ctx, db.Namespace{})
	if err != nil {
		return c, err
	}
	for _, t := range tables {
		c.Tables = append(c.Tables, db.CatalogTable{Name: t.Name, Kind: t.Kind})
	}
	rs, err := m.c.Query(ctx, `SELECT m.name, p.name, p.type FROM sqlite_master m, pragma_table_xinfo(m.name) p
		WHERE m.type IN ('table', 'view') AND m.name NOT LIKE 'sqlite\_%' ESCAPE '\' AND p.hidden <> 1 ORDER BY m.name, p.cid`)
	if err != nil {
		return c, err
	}
	for _, r := range rs.Rows {
		typ := r[2].Text
		if typ == "" {
			typ = anyType
		}
		c.Columns = append(c.Columns, db.CatalogColumn{Table: db.TableRef{Name: r[0].Text}, Name: r[1].Text, Type: typ})
	}
	return c, nil
}

// RoutineDDL: SQLite has no stored routines.
func (meta) RoutineDDL(context.Context, db.Routine) (string, error) { return "", db.ErrNotFound }
