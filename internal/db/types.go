package db

// Namespace locates tables. MySQL leaves Schema empty.
type Namespace struct {
	Database string
	Schema   string
}

// TableRef identifies a table or view.
type TableRef struct {
	Namespace
	Name string
}

// TableKind distinguishes tables from views.
type TableKind int

const (
	TableKindTable TableKind = iota
	TableKindView
)

// Table is a table or view listed in a namespace.
type Table struct {
	Name    string
	Kind    TableKind
	Comment string
}

// Column describes one table column. The fields after PrimaryKey are only
// filled by MetaReader.TableSchema.
type Column struct {
	Name       string
	Type       string
	Nullable   bool
	Default    *string
	Comment    string
	PrimaryKey bool

	AutoIncrement bool   // MySQL auto_increment; PostgreSQL identity or serial
	Collation     string // only when it is not the default (the table's on MySQL, the type's on PostgreSQL)
	Generated     string // the expression of a generated column
	Position      int    // 1-based; shown, never compared
}

// Index describes one index; Primary implies Unique. The fields after
// Primary are only filled by MetaReader.TableSchema.
type Index struct {
	Name    string
	Columns []string
	Unique  bool
	Primary bool

	Method     string // btree, hash, gin, fulltext, … in lower case
	Where      string // the predicate of a PostgreSQL partial index
	Constraint bool   // PostgreSQL: the index backs a PRIMARY KEY or UNIQUE constraint
}

// ForeignKey describes one foreign-key constraint.
type ForeignKey struct {
	Name       string
	Columns    []string
	RefTable   TableRef
	RefColumns []string
}

// TableSchema is a table as schema comparison sees it. MetaReader.TableSchema
// normalizes it so that equal definitions compare equal: Type is lower case
// with the engine's short names, Default is SQL text that can follow
// DEFAULT, and auto-increment and generated columns have no Default.
type TableSchema struct {
	Ref         TableRef
	Columns     []Column
	Indexes     []Index
	ForeignKeys []ForeignKey
	Comment     string
	Options     map[string]string // MySQL: "engine" and "collation"; PostgreSQL: none
}

// ColumnInfo describes a result-set column.
type ColumnInfo struct {
	Name         string
	DatabaseType string
}

// Value is one cell. Text is the server's text representation; Null
// distinguishes SQL NULL from the empty string.
type Value struct {
	Text string
	Null bool
}

// ResultSet is a fully read query result.
type ResultSet struct {
	Columns []ColumnInfo
	Rows    [][]Value
}

// ExecResult is the outcome of a statement that returns no rows.
type ExecResult struct {
	RowsAffected int64
}

// RoutineKind distinguishes stored functions from procedures.
type RoutineKind int

const (
	RoutineFunction RoutineKind = iota
	RoutineProcedure
)

// Routine is a stored function or procedure. Args is PostgreSQL's identity
// argument list (it tells overloads apart); MySQL leaves it empty.
type Routine struct {
	Namespace Namespace
	Name      string
	Kind      RoutineKind
	Args      string
}

// CatalogTable is a table or view with its namespace.
type CatalogTable struct {
	Namespace Namespace
	Name      string
	Kind      TableKind
}

// CatalogColumn is one column of a catalog table.
type CatalogColumn struct {
	Table TableRef
	Name  string
	Type  string
}

// Catalog is every user object a Conn can see, read in bulk for search and
// completion. System databases and schemas are left out of Namespaces,
// Tables, Columns and Routines.
type Catalog struct {
	Databases  []string    // every database on the server, as Databases returns them
	Namespaces []Namespace // MySQL: one per user database; PostgreSQL: one per user schema of the Conn's database
	Tables     []CatalogTable
	Columns    []CatalogColumn // grouped by table, in column order
	Routines   []Routine
}
