package sqlx

import "strings"

// RowLocator is how staged edits find a row of a table without a usable key.
type RowLocator int

const (
	LocateNone       RowLocator = iota
	LocateAllColumns            // match every comparable column, one row at most
	LocateRowID                 // the engine's physical row id (ctid, ROWID)
)

// Features describes what an engine can do, so callers ask about a
// capability instead of comparing driver names.
type Features struct {
	Databases      bool       // several databases per server: the tree has a database level
	Schemas        bool       // namespaces have a schema level
	ServerReadOnly bool       // a connection can be made read-only on the server
	TxDDL          bool       // DDL can be rolled back inside a transaction
	AbortedTx      bool       // after an error in a transaction only ROLLBACK works
	DDLCommits     bool       // DDL implicitly commits the open transaction
	SchemaTools    bool       // Modify Table, schema/data compare and import
	ILike          bool       // ILIKE is available
	RowLocator     RowLocator // how to find a row without a key
	DefaultSchema  string     // schema used when none is given; "" is the current user
	// FallbackDatabase is the database a database-scoped connection without
	// one configured opens.
	FallbackDatabase string
}

var features = [...]Features{
	MySQL: {
		Databases: true, ServerReadOnly: true, DDLCommits: true, SchemaTools: true,
		RowLocator: LocateAllColumns,
	},
	Postgres: {
		Databases: true, Schemas: true, ServerReadOnly: true, TxDDL: true, AbortedTx: true,
		SchemaTools: true, ILike: true, RowLocator: LocateRowID, DefaultSchema: "public",
		FallbackDatabase: "postgres",
	},
	SQLServer: {
		Databases: true, Schemas: true, TxDDL: true,
		RowLocator: LocateAllColumns, DefaultSchema: "dbo",
		FallbackDatabase: "master",
	},
	Oracle: {
		Schemas: true, DDLCommits: true, RowLocator: LocateRowID,
	},
}

// Features returns the capabilities of d.
func (d Dialect) Features() Features { return features[d] }

// RowIDSelect selects every row of the table named name (quoted) with its
// row id as text ahead of its columns, on an engine that locates rows by
// row id: ctid on PostgreSQL, ROWID on Oracle (which wants an alias).
func RowIDSelect(d Dialect, name string) string {
	if d == Oracle {
		return "SELECT ROWIDTOCHAR(t.ROWID), t.* FROM " + name + " t"
	}
	return "SELECT ctid::text, * FROM " + name
}

// RowIDMatch is the condition that finds the row whose id RowIDSelect read.
func RowIDMatch(d Dialect, id string) string {
	lit := "'" + strings.ReplaceAll(id, "'", "''") + "'"
	if d == Oracle {
		return "ROWID = CHARTOROWID(" + lit + ")"
	}
	return "ctid = " + lit
}

// RowIDName names the row id in messages.
func RowIDName(d Dialect) string {
	if d == Oracle {
		return "ROWID"
	}
	return "ctid"
}
