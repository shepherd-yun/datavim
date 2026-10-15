package mysql

import "github.com/shepherd-yun/sqlvim/internal/db"

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
