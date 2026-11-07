package sqlite

import (
	"regexp"
	"slices"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
)

// ddlWriter writes the DDL SQLite has natively. There is no rebuild ("new
// table, copy, swap"): other changes come back as manual.
type ddlWriter struct{ d dialect }

func (w ddlWriter) DropTable(t db.TableRef, view bool) string {
	if view {
		return "DROP VIEW " + w.d.QualifiedName(t)
	}
	return "DROP TABLE " + w.d.QualifiedName(t)
}

// RenameTable also writes ALTER TABLE for a view: SQLite cannot rename one, and
// the server's message says so.
func (w ddlWriter) RenameTable(t db.TableRef, newName string, _ bool) string {
	return "ALTER TABLE " + w.d.QualifiedName(t) + " RENAME TO " + w.d.QuoteIdent(newName)
}

func (w ddlWriter) TruncateTable(t db.TableRef) string { return "DELETE FROM " + w.d.QualifiedName(t) }

func (ddlWriter) NormalizeType(t string) string { return normalizeType(t) }

// columnDef is a column as CREATE TABLE and ADD COLUMN write it. Comments do
// not exist on SQLite.
func (w ddlWriter) columnDef(c db.Column) string {
	s := w.d.QuoteIdent(c.Name)
	if c.Type != "" && c.Type != "any" {
		s += " " + c.Type
	}
	if c.AutoIncrement {
		return s + " PRIMARY KEY AUTOINCREMENT"
	}
	if c.Collation != "" {
		s += " COLLATE " + c.Collation
	}
	if c.Generated != "" {
		s += " GENERATED ALWAYS AS (" + c.Generated + ")"
	}
	if !c.Nullable {
		s += " NOT NULL"
	}
	if c.Default != nil && c.Generated == "" {
		s += " DEFAULT " + defaultValue(*c.Default)
	}
	return s
}

// bareDefault is what DEFAULT takes without parentheses: a number, a string, a
// blob, or a word (NULL, TRUE, CURRENT_TIMESTAMP, or an identifier read as text).
var bareDefault = regexp.MustCompile(`^(?:[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?|[+-]?0[xX][0-9a-fA-F]+|'(?:[^']|'')*'|"(?:[^"]|"")*"|[xX]'[0-9a-fA-F]*'|[\p{L}_][\p{L}\p{N}_$]*)$`)

// defaultValue is a default as DEFAULT writes it. PRAGMA table_info reports an
// expression without the parentheses it needs.
func defaultValue(d string) string {
	if bareDefault.MatchString(d) {
		return d
	}
	return "(" + d + ")"
}

// indexColumns quotes plain names, keeps expressions as they are, and adds what
// follows each entry.
func (w ddlWriter) indexColumns(ix db.Index) string {
	parts := make([]string, len(ix.Columns))
	for i, c := range ix.Columns {
		if ix.IsExpression(c) {
			parts[i] = c
		} else {
			parts[i] = w.d.QuoteIdent(c)
		}
		if i < len(ix.Suffixes) && ix.Suffixes[i] != "" {
			parts[i] += " " + ix.Suffixes[i]
		}
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func (w ddlWriter) createIndex(t db.TableRef, ix db.Index) string {
	s := "CREATE "
	if ix.Unique {
		s += "UNIQUE "
	}
	s += "INDEX " + w.d.QuoteIdent(ix.Name) + " ON " + w.d.QualifiedName(t) + " " + w.indexColumns(ix)
	if ix.Where != "" {
		s += " WHERE " + ix.Where
	}
	return s
}

// internalName reports a name only SQLite may give.
func internalName(name string) bool { return strings.HasPrefix(strings.ToLower(name), "sqlite_") }

// primaryConstraint is "CONSTRAINT name " for a primary key that has a name.
func (w ddlWriter) primaryConstraint(ix db.Index) string {
	if ix.Name != "" && ix.Name != primaryName && !internalName(ix.Name) {
		return "CONSTRAINT " + w.d.QuoteIdent(ix.Name) + " "
	}
	return ""
}

// descendingKey returns the primary key that must be written with its column:
// INTEGER PRIMARY KEY DESC is no rowid alias there, but PRIMARY KEY (x DESC)
// would be one.
func descendingKey(s db.TableSchema, withoutRowid bool) (db.Index, string, bool) {
	if withoutRowid {
		return db.Index{}, "", false
	}
	for _, ix := range s.Indexes {
		if !ix.Primary || len(ix.Columns) != 1 || !slices.Equal(ix.Suffixes, []string{"DESC"}) {
			continue
		}
		for _, c := range s.Columns {
			if c.Name == ix.Columns[0] && strings.EqualFold(c.Type, "INTEGER") {
				return ix, c.Name, true
			}
		}
	}
	return db.Index{}, "", false
}

func (w ddlWriter) CreateTable(s db.TableSchema) ([]string, []db.SchemaChange) {
	lines := make([]string, 0, len(s.Columns)+1)
	options := s.Options[tableOptions]
	key, keyColumn, inline := descendingKey(s, strings.Contains(options, "WITHOUT ROWID"))
	auto := false
	for _, c := range s.Columns {
		auto = auto || c.AutoIncrement
		if c.Type == "any" && strings.Contains(options, "STRICT") {
			c.Type = anyType // columnDef leaves out normalized "any", which STRICT requires
		}
		def := w.columnDef(c)
		if inline && c.Name == keyColumn {
			def += " " + w.primaryConstraint(key) + "PRIMARY KEY DESC"
		}
		lines = append(lines, def)
	}
	var indexes []string
	for _, ix := range s.Indexes {
		switch {
		case ix.Primary && (auto || inline): // written with its column
		case ix.Primary:
			lines = append(lines, w.primaryConstraint(ix)+"PRIMARY KEY "+w.indexColumns(ix))
		case internalName(ix.Name): // a UNIQUE constraint's own index
			lines = append(lines, "UNIQUE "+w.indexColumns(ix))
		default:
			indexes = append(indexes, w.createIndex(s.Ref, ix))
		}
	}
	create := "CREATE TABLE " + w.d.QualifiedName(s.Ref) + " (\n  " + strings.Join(lines, ",\n  ") + "\n)"
	if options != "" {
		create += " " + options
	}
	return append([]string{create}, indexes...), nil
}

// AlterTable writes one statement per column or index change. Anything else
// (a column's type, default, NOT NULL; primary keys, constraints' own indexes,
// foreign keys, comments, table options) needs a rebuild, which datavim does not do.
func (w ddlWriter) AlterTable(t db.TableRef, changes []db.SchemaChange) ([]string, []db.SchemaChange) {
	q := w.d.QuoteIdent
	table := "ALTER TABLE " + w.d.QualifiedName(t) + " "
	var stmts []string
	var manual []db.SchemaChange
	for _, c := range changes {
		switch {
		case c.Kind == db.AddColumn:
			stmts = append(stmts, table+"ADD COLUMN "+w.columnDef(*c.New))
		case c.Kind == db.DropColumn:
			stmts = append(stmts, table+"DROP COLUMN "+q(c.Old.Name))
		case c.Kind == db.RenameColumn:
			stmts = append(stmts, table+"RENAME COLUMN "+q(c.Old.Name)+" TO "+q(c.New.Name))
		case c.Kind == db.AddIndex && !c.NewIndex.Primary && !internalName(c.NewIndex.Name):
			stmts = append(stmts, w.createIndex(t, *c.NewIndex))
		case c.Kind == db.DropIndex && !c.OldIndex.Primary && !internalName(c.OldIndex.Name):
			stmts = append(stmts, "DROP INDEX "+q(c.OldIndex.Name))
		default:
			manual = append(manual, c)
		}
	}
	return stmts, manual
}
