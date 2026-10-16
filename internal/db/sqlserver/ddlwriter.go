package sqlserver

import (
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

type ddlWriter struct{ d dialect }

func (d dialect) DDL() db.DDLWriter { return ddlWriter{d} }

func (w ddlWriter) DropTable(t db.TableRef, view bool) string {
	if view {
		return "DROP VIEW " + w.d.QualifiedName(t)
	}
	return "DROP TABLE " + w.d.QualifiedName(t)
}

// RenameTable calls sp_rename: the old name is quoted SQL, the new one a
// plain name.
func (w ddlWriter) RenameTable(t db.TableRef, newName string, _ bool) string {
	lit := func(s string) string { return sqlx.Literal(sqlx.SQLServer, "NVARCHAR", db.Value{Text: s}) }
	return "EXEC sp_rename " + lit(w.d.QualifiedName(t)) + ", " + lit(newName)
}

func (w ddlWriter) TruncateTable(t db.TableRef) string {
	return "TRUNCATE TABLE " + w.d.QualifiedName(t)
}

// CreateTable and AlterTable are schema tools, which SQL Server does not
// have yet (sqlx.Features.SchemaTools): everything is left to do by hand.
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
