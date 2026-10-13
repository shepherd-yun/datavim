package meta

import "github.com/shepherd-yun/sqlvim/internal/db"

// Item is one searchable object (filled in by the search task).
type Item struct{}

func buildItems(Key, db.Catalog) []Item { return nil }
