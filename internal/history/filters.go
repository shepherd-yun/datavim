package history

import "time"

// MaxFilters is how many filters Filters keeps per key.
const MaxFilters = 50

var filterSchema = []string{
	`CREATE TABLE IF NOT EXISTS filters (
		key  TEXT NOT NULL,
		text TEXT NOT NULL,
		at   INTEGER NOT NULL,
		PRIMARY KEY (key, text)
	)`,
}

// AddFilter remembers a filter typed for key (an Elasticsearch
// connection and index); the oldest past MaxFilters are pruned.
func (s *Store) AddFilter(key, text string) error {
	if _, err := s.db.Exec(`INSERT INTO filters (key, text, at) VALUES (?, ?, ?)
		ON CONFLICT (key, text) DO UPDATE SET at = excluded.at`, key, text, time.Now().UnixNano()); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM filters WHERE key = ? AND text NOT IN
		(SELECT text FROM filters WHERE key = ? ORDER BY at DESC LIMIT ?)`, key, key, MaxFilters)
	return err
}

// Filters are key's filters, newest first.
func (s *Store) Filters(key string, limit int) ([]string, error) {
	rows, err := s.db.Query(`SELECT text FROM filters WHERE key = ? ORDER BY at DESC LIMIT ?`, key, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
