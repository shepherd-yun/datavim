// Package meta caches database catalogs for search and completion.
package meta

import (
	"strings"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// Key names one cached catalog: a connection and, for PostgreSQL, the
// database (MySQL catalogs cover the whole server and leave it empty).
type Key struct {
	Conn     string
	Database string
}

// Snapshot is a loaded catalog. It is never changed after NewSnapshot, so
// commands may read it on other goroutines.
type Snapshot struct {
	Key      Key
	Catalog  db.Catalog
	LoadedAt time.Time

	tables  map[db.Namespace][]db.CatalogTable
	columns map[db.TableRef][]db.CatalogColumn
	items   []Item
}

// NewSnapshot indexes c.
func NewSnapshot(k Key, c db.Catalog, at time.Time) *Snapshot {
	s := &Snapshot{
		Key: k, Catalog: c, LoadedAt: at,
		tables:  map[db.Namespace][]db.CatalogTable{},
		columns: map[db.TableRef][]db.CatalogColumn{},
	}
	for _, t := range c.Tables {
		s.tables[t.Namespace] = append(s.tables[t.Namespace], t)
	}
	for _, col := range c.Columns {
		s.columns[col.Table] = append(s.columns[col.Table], col)
	}
	s.items = buildItems(k, c)
	return s
}

// Tables lists the tables and views of ns. Without an exact match the
// namespace is matched ignoring case (unquoted names fold case).
func (s *Snapshot) Tables(ns db.Namespace) []db.CatalogTable {
	if t, ok := s.tables[ns]; ok {
		return t
	}
	for k, t := range s.tables {
		if strings.EqualFold(k.Database, ns.Database) && strings.EqualFold(k.Schema, ns.Schema) {
			return t
		}
	}
	return nil
}

// FindTable resolves a table name as typed: exactly, else ignoring case.
func (s *Snapshot) FindTable(ns db.Namespace, name string) (db.CatalogTable, bool) {
	tables := s.Tables(ns)
	for _, t := range tables {
		if t.Name == name {
			return t, true
		}
	}
	for _, t := range tables {
		if strings.EqualFold(t.Name, name) {
			return t, true
		}
	}
	return db.CatalogTable{}, false
}

// Columns lists the columns of t in order (t resolved as FindTable does).
func (s *Snapshot) Columns(t db.TableRef) []db.CatalogColumn {
	tb, ok := s.FindTable(t.Namespace, t.Name)
	if !ok {
		return nil
	}
	return s.columns[db.TableRef{Namespace: tb.Namespace, Name: tb.Name}]
}
