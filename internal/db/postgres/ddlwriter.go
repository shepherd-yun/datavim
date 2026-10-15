package postgres

import "github.com/shepherd-yun/sqlvim/internal/db"

func (dialect) DDL() db.DDLWriter { return ddlWriter{} }

// ddlWriter writes PostgreSQL DDL.
type ddlWriter struct{ d dialect }

func (w ddlWriter) DropTable(t db.TableRef, view bool) string {
	if view {
		return "DROP VIEW " + w.d.QualifiedName(t)
	}
	return "DROP TABLE " + w.d.QualifiedName(t)
}

// RenameTable keeps the schema: RENAME TO takes a bare name.
func (w ddlWriter) RenameTable(t db.TableRef, newName string, view bool) string {
	kw := "ALTER TABLE "
	if view {
		kw = "ALTER VIEW "
	}
	return kw + w.d.QualifiedName(t) + " RENAME TO " + w.d.QuoteIdent(newName)
}

func (w ddlWriter) TruncateTable(t db.TableRef) string {
	return "TRUNCATE TABLE " + w.d.QualifiedName(t)
}
