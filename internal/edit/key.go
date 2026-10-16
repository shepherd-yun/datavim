package edit

import (
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// KeyKind says how the rows of a table are located.
type KeyKind int

const (
	NoKey      KeyKind = iota // rows cannot be located: only new rows can be added
	PrimaryKey                // the primary key
	UniqueKey                 // a unique index over NOT NULL columns
	AllColumns                // MySQL without a key: every comparable column, LIMIT 1
	RowID                     // PostgreSQL without a key: ctid
)

// Key is how rows are located.
type Key struct {
	Kind    KeyKind
	Columns []string // PrimaryKey and UniqueKey: the key's columns
}

// ChooseKey picks how rows of a table are located: its primary key, else a
// unique index over NOT NULL columns, else the engine's fallback
// (sqlx.Features.RowLocator): the row id of a table (PostgreSQL ctid,
// Oracle ROWID) or every column (MySQL, SQL Server). A view without a key
// on a row-id engine has none.
func ChooseKey(d sqlx.Dialect, cols []db.Column, idx []db.Index, view bool) Key {
	nullable := make(map[string]bool, len(cols))
	var pk []string
	for _, c := range cols {
		nullable[c.Name] = c.Nullable
		if c.PrimaryKey {
			pk = append(pk, c.Name)
		}
	}
	for _, ix := range idx {
		if ix.Primary && len(ix.Columns) > 0 {
			pk = ix.Columns
		}
	}
	if len(pk) > 0 {
		return Key{Kind: PrimaryKey, Columns: pk}
	}
	for _, ix := range idx {
		if ix.Unique && notNull(ix.Columns, nullable) {
			return Key{Kind: UniqueKey, Columns: ix.Columns}
		}
	}
	switch d.Features().RowLocator {
	case sqlx.LocateRowID:
		if !view {
			return Key{Kind: RowID}
		}
	case sqlx.LocateAllColumns:
		return Key{Kind: AllColumns}
	case sqlx.LocateNone:
	}
	return Key{}
}

// notNull reports names that are all known NOT NULL columns.
func notNull(names []string, nullable map[string]bool) bool {
	if len(names) == 0 {
		return false
	}
	for _, n := range names {
		if null, known := nullable[n]; !known || null {
			return false
		}
	}
	return true
}
