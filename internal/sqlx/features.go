package sqlx

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
