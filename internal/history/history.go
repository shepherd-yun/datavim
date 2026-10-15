// Package history stores SQL history and console drafts in a local SQLite file.
package history

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// maxEntries bounds the history table; older entries are pruned.
const maxEntries = 10000

// maxRecent bounds the recent table; the oldest rows are pruned.
const maxRecent = 200

// Kinds of Recent entries.
const (
	RecentData      = "data"
	RecentStructure = "structure"
	RecentConsole   = "console"
	RecentFile      = "file" // Name holds the file's absolute path
)

// Recent is a table, structure view or console the user opened.
type Recent struct {
	Kind     string
	Conn     string
	Database string
	Schema   string
	Name     string // the table; empty for consoles
	Time     time.Time
}

// Entry is one executed statement.
type Entry struct {
	Time     time.Time
	Conn     string
	Database string
	SQL      string
	Duration time.Duration
	Err      string // empty on success
}

// Store is the state database. Safe for concurrent use.
type Store struct{ db *sql.DB }

var schema = []string{
	`CREATE TABLE IF NOT EXISTS history (
		id          INTEGER PRIMARY KEY,
		at          INTEGER NOT NULL,
		conn        TEXT NOT NULL,
		db_name     TEXT NOT NULL,
		sql         TEXT NOT NULL,
		duration_ms INTEGER NOT NULL,
		error       TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS drafts (
		key     TEXT PRIMARY KEY,
		text    TEXT NOT NULL,
		updated INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS recent (
		kind        TEXT NOT NULL,
		conn        TEXT NOT NULL,
		db_name     TEXT NOT NULL,
		schema_name TEXT NOT NULL,
		name        TEXT NOT NULL,
		at          INTEGER NOT NULL,
		PRIMARY KEY (kind, conn, db_name, schema_name, name)
	)`,
}

// Open opens or creates the state database at path (mode 0600).
// ":memory:" opens a private in-memory database.
func Open(path string) (*Store, error) {
	dsn := ":memory:"
	if path != ":memory:" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		_ = f.Close()
		dsn = path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqldb.SetMaxOpenConns(1) // a single writer; also keeps :memory: one database
	for _, stmt := range schema {
		if _, err := sqldb.Exec(stmt); err != nil {
			_ = sqldb.Close()
			return nil, err
		}
	}
	return &Store{db: sqldb}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// AddHistory records one statement and prunes old entries.
func (s *Store) AddHistory(e Entry) error {
	_, err := s.db.Exec(`INSERT INTO history (at, conn, db_name, sql, duration_ms, error) VALUES (?, ?, ?, ?, ?, ?)`,
		e.Time.UnixMilli(), e.Conn, e.Database, e.SQL, e.Duration.Milliseconds(), e.Err)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM history WHERE id <= (SELECT max(id) FROM history) - ?`, maxEntries)
	return err
}

// History returns up to limit entries whose SQL contains filter, newest first.
func (s *Store) History(filter string, limit int) ([]Entry, error) {
	rows, err := s.db.Query(`SELECT at, conn, db_name, sql, duration_ms, error FROM history
		WHERE sql LIKE ? ESCAPE '\' ORDER BY id DESC LIMIT ?`, "%"+escapeLike(filter)+"%", limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Entry
	for rows.Next() {
		var e Entry
		var at, ms int64
		if err := rows.Scan(&at, &e.Conn, &e.Database, &e.SQL, &ms, &e.Err); err != nil {
			return nil, err
		}
		e.Time = time.UnixMilli(at).UTC()
		e.Duration = time.Duration(ms) * time.Millisecond
		out = append(out, e)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// SaveDraft stores the console text for key; empty text deletes the draft.
func (s *Store) SaveDraft(key, text string) error {
	if text == "" {
		_, err := s.db.Exec(`DELETE FROM drafts WHERE key = ?`, key)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO drafts (key, text, updated) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET text = excluded.text, updated = excluded.updated`,
		key, text, time.Now().UnixMilli())
	return err
}

// Draft returns the stored console text for key, or "".
func (s *Store) Draft(key string) (string, error) {
	var text string
	err := s.db.QueryRow(`SELECT text FROM drafts WHERE key = ?`, key).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return text, err
}

// AddRecent records that r was opened at r.Time; an existing entry for the
// same object moves to that time. Old entries are pruned.
func (s *Store) AddRecent(r Recent) error {
	_, err := s.db.Exec(`INSERT INTO recent (kind, conn, db_name, schema_name, name, at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (kind, conn, db_name, schema_name, name) DO UPDATE SET at = excluded.at`,
		r.Kind, r.Conn, r.Database, r.Schema, r.Name, r.Time.UnixMilli())
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM recent WHERE rowid NOT IN
		(SELECT rowid FROM recent ORDER BY at DESC, rowid DESC LIMIT ?)`, maxRecent)
	return err
}

// Recent returns up to limit entries, most recently opened first.
func (s *Store) Recent(limit int) ([]Recent, error) {
	rows, err := s.db.Query(`SELECT kind, conn, db_name, schema_name, name, at FROM recent
		ORDER BY at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Recent
	for rows.Next() {
		var r Recent
		var at int64
		if err := rows.Scan(&r.Kind, &r.Conn, &r.Database, &r.Schema, &r.Name, &at); err != nil {
			return nil, err
		}
		r.Time = time.UnixMilli(at).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
