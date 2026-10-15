package db

// ChangeKind is the kind of one SchemaChange.
type ChangeKind int

const (
	AddColumn ChangeKind = iota
	DropColumn
	AlterColumn
	RenameColumn
	AddIndex
	DropIndex
	AlterTableComment
	AlterTableOption
	// ForeignKeyDiff is shown, never synced.
	ForeignKeyDiff
)

var changeKindNames = [...]string{
	AddColumn: "AddColumn", DropColumn: "DropColumn", AlterColumn: "AlterColumn", RenameColumn: "RenameColumn",
	AddIndex: "AddIndex", DropIndex: "DropIndex", AlterTableComment: "AlterTableComment",
	AlterTableOption: "AlterTableOption", ForeignKeyDiff: "ForeignKeyDiff",
}

func (k ChangeKind) String() string { return changeKindNames[k] }

// ColumnAttr flags what an AlterColumn changes.
type ColumnAttr uint8

const (
	AttrType ColumnAttr = 1 << iota
	AttrNullable
	AttrDefault
	AttrComment
	AttrCollation
	AttrAutoIncrement
	AttrGenerated
)

// Risk flags changes that can lose data.
type Risk uint8

const (
	// Destructive changes delete something: a table, a column, a unique index.
	Destructive Risk = 1 << iota
	// Lossy changes can fail on, or cut, the data a table holds: a narrower
	// type, NOT NULL, a new unique index.
	Lossy
)

// SchemaChange is one change to a table, computed by package schemadiff and
// written as SQL by a DDLWriter. Old is what the table has, New what it
// should have.
type SchemaChange struct {
	Kind     ChangeKind
	Old, New *Column    // AddColumn: New; DropColumn: Old; AlterColumn and RenameColumn: both
	Attrs    ColumnAttr // AlterColumn: what differs
	// After is, for AddColumn, the column the new one follows in the
	// source; "" puts it first.
	After              string
	OldIndex, NewIndex *Index      // DropIndex: OldIndex; AddIndex: NewIndex
	OldFK, NewFK       *ForeignKey // ForeignKeyDiff: nil on the side that lacks it
	Option             string      // AlterTableOption: the key in TableSchema.Options
	OldValue, NewValue string      // AlterTableComment and AlterTableOption
	Risk               Risk
}
