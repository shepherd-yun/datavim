package mysql

import (
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func (dialect) DDL() db.DDLWriter { return ddlWriter{} }

// ddlWriter writes MySQL DDL.
type ddlWriter struct{ d dialect }

func (w ddlWriter) DropTable(t db.TableRef, view bool) string {
	if view {
		return "DROP VIEW " + w.d.QualifiedName(t)
	}
	return "DROP TABLE " + w.d.QualifiedName(t)
}

// RenameTable uses RENAME TABLE, which MySQL accepts for views too.
func (w ddlWriter) RenameTable(t db.TableRef, newName string, _ bool) string {
	to := t
	to.Name = newName
	return "RENAME TABLE " + w.d.QualifiedName(t) + " TO " + w.d.QualifiedName(to)
}

func (w ddlWriter) TruncateTable(t db.TableRef) string {
	return "TRUNCATE TABLE " + w.d.QualifiedName(t)
}

// columnDef is a column as CREATE TABLE and ALTER TABLE write it. NULL is
// spelled out: a TIMESTAMP column is NOT NULL unless told otherwise.
func (w ddlWriter) columnDef(c db.Column) string {
	var b strings.Builder
	b.WriteString(w.d.QuoteIdent(c.Name) + " " + c.Type)
	if c.Collation != "" {
		b.WriteString(" COLLATE " + c.Collation)
	}
	if c.Generated != "" {
		b.WriteString(" GENERATED ALWAYS AS (" + c.Generated + ") STORED")
	}
	if c.Nullable {
		b.WriteString(" NULL")
	} else {
		b.WriteString(" NOT NULL")
	}
	if c.Default != nil && c.Generated == "" {
		b.WriteString(" DEFAULT " + *c.Default)
	}
	if c.OnUpdate != "" {
		b.WriteString(" ON UPDATE " + c.OnUpdate)
	}
	if c.AutoIncrement {
		b.WriteString(" AUTO_INCREMENT")
	}
	if c.Comment != "" {
		b.WriteString(" COMMENT " + quoteString(c.Comment))
	}
	return b.String()
}

// indexDef is ix as CREATE TABLE lists it and ALTER TABLE ADD takes it; ok
// is false for expression indexes, whose expression information_schema
// does not give.
func (w ddlWriter) indexDef(ix db.Index) (def string, ok bool) {
	cols := make([]string, len(ix.Columns))
	for i, c := range ix.Columns {
		if c == "(expression)" {
			return "", false
		}
		cols[i] = w.d.QuoteIdent(c)
	}
	list := "(" + strings.Join(cols, ", ") + ")"
	name := w.d.QuoteIdent(ix.Name)
	switch {
	case ix.Primary:
		return "PRIMARY KEY " + list, true
	case ix.Method == "fulltext":
		return "FULLTEXT INDEX " + name + " " + list, true
	case ix.Method == "spatial":
		return "SPATIAL INDEX " + name + " " + list, true
	case ix.Unique:
		return "UNIQUE INDEX " + name + " " + list, true
	}
	return "INDEX " + name + " " + list, true
}

func (w ddlWriter) CreateTable(s db.TableSchema) ([]string, []db.SchemaChange) {
	lines := make([]string, 0, len(s.Columns)+len(s.Indexes))
	for _, c := range s.Columns {
		lines = append(lines, "  "+w.columnDef(c))
	}
	var manual []db.SchemaChange
	for i := range s.Indexes {
		def, ok := w.indexDef(s.Indexes[i])
		if !ok {
			manual = append(manual, db.SchemaChange{Kind: db.AddIndex, NewIndex: &s.Indexes[i]})
			continue
		}
		lines = append(lines, "  "+def)
	}
	stmt := "CREATE TABLE " + w.d.QualifiedName(s.Ref) + " (\n" + strings.Join(lines, ",\n") + "\n)"
	if e := s.Options["engine"]; e != "" {
		stmt += " ENGINE=" + e
	}
	if c := s.Options["collation"]; c != "" {
		stmt += " COLLATE=" + c
	}
	if s.Comment != "" {
		stmt += " COMMENT=" + quoteString(s.Comment)
	}
	return []string{stmt}, manual
}

func (w ddlWriter) AlterTable(t db.TableRef, changes []db.SchemaChange) ([]string, []db.SchemaChange) {
	// A column renamed and altered at once is one CHANGE COLUMN.
	altered := map[string]bool{}
	for _, c := range changes {
		if c.Kind == db.AlterColumn {
			altered[c.Old.Name] = true
		}
	}
	var clauses []string
	var manual []db.SchemaChange
	for _, c := range changes {
		if c.Kind == db.RenameColumn && altered[c.Old.Name] {
			continue
		}
		clause, ok := w.alterClause(c)
		if !ok {
			manual = append(manual, c)
			continue
		}
		clauses = append(clauses, clause)
	}
	if len(clauses) == 0 {
		return nil, manual
	}
	return []string{"ALTER TABLE " + w.d.QualifiedName(t) + "\n  " + strings.Join(clauses, ",\n  ")}, manual
}

func (w ddlWriter) alterClause(c db.SchemaChange) (string, bool) {
	q := w.d.QuoteIdent
	switch c.Kind {
	case db.AddColumn:
		pos := " FIRST"
		if c.After != "" {
			pos = " AFTER " + q(c.After)
		}
		return "ADD COLUMN " + w.columnDef(*c.New) + pos, true
	case db.DropColumn:
		return "DROP COLUMN " + q(c.Old.Name), true
	case db.RenameColumn:
		return "RENAME COLUMN " + q(c.Old.Name) + " TO " + q(c.New.Name), true
	case db.AlterColumn:
		switch {
		case c.Attrs&db.AttrGenerated != 0:
			return "", false
		case c.Old.Name != c.New.Name:
			return "CHANGE COLUMN " + q(c.Old.Name) + " " + w.columnDef(*c.New), true
		}
		return "MODIFY COLUMN " + w.columnDef(*c.New), true
	case db.AddIndex:
		def, ok := w.indexDef(*c.NewIndex)
		return "ADD " + def, ok
	case db.DropIndex:
		if c.OldIndex.Primary {
			return "DROP PRIMARY KEY", true
		}
		return "DROP INDEX " + q(c.OldIndex.Name), true
	case db.AlterTableComment:
		return "COMMENT = " + quoteString(c.NewValue), true
	case db.AlterTableOption:
		switch c.Option {
		case "engine":
			return "ENGINE = " + c.NewValue, true
		case "collation":
			return "COLLATE = " + c.NewValue, true
		}
	}
	return "", false
}
