package sqlite

import (
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

type dialect struct{}

func (dialect) QuoteIdent(s string) string { return sqlx.QuoteIdent(sqlx.SQLite, s) }

// QualifiedName is the bare table: a connection is one file.
func (dialect) QualifiedName(t db.TableRef) string { return sqlx.QualifiedName(sqlx.SQLite, t) }

func (dialect) Paginate(query string, limit, offset int) string {
	return db.LimitOffset(query, limit, offset)
}

func (dialect) DDL() db.DDLWriter { return ddlWriter{} }
