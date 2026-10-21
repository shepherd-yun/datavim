package oracle

import (
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
)

type ddlWriter struct{ d dialect }

func (d dialect) DDL() db.DDLWriter { return ddlWriter{d} }

func (w ddlWriter) DropTable(t db.TableRef, view bool) string {
	if view {
		return "DROP VIEW " + w.d.QualifiedName(t)
	}
	return "DROP TABLE " + w.d.QualifiedName(t)
}

// RenameTable renames a table with ALTER TABLE. Views have only RENAME,
// which works in the current schema: the console's or the connected user's.
func (w ddlWriter) RenameTable(t db.TableRef, newName string, view bool) string {
	if view {
		return "RENAME " + w.d.QuoteIdent(t.Name) + " TO " + w.d.QuoteIdent(newName)
	}
	return "ALTER TABLE " + w.d.QualifiedName(t) + " RENAME TO " + w.d.QuoteIdent(newName)
}

func (w ddlWriter) TruncateTable(t db.TableRef) string {
	return "TRUNCATE TABLE " + w.d.QualifiedName(t)
}

// CreateTable and AlterTable are schema tools, which Oracle does not have
// yet (sqlx.Features.SchemaTools): everything is left to do by hand.
func (ddlWriter) CreateTable(s db.TableSchema) ([]string, []db.SchemaChange) {
	manual := make([]db.SchemaChange, 0, len(s.Columns)+len(s.Indexes))
	for i := range s.Columns {
		manual = append(manual, db.SchemaChange{Kind: db.AddColumn, New: &s.Columns[i]})
	}
	for i := range s.Indexes {
		manual = append(manual, db.SchemaChange{Kind: db.AddIndex, NewIndex: &s.Indexes[i]})
	}
	return nil, manual
}

func (ddlWriter) AlterTable(_ db.TableRef, changes []db.SchemaChange) ([]string, []db.SchemaChange) {
	return nil, changes
}

// NormalizeType lower-cases t and drops blanks.
func (ddlWriter) NormalizeType(t string) string {
	return strings.ToLower(strings.Join(strings.Fields(t), ""))
}
