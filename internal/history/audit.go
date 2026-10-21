package history

import (
	"strings"
	"time"
)

// maxAudit bounds the audit table; the oldest rows are pruned. A variable
// so tests can lower it.
var maxAudit = 50000

// Kinds of AuditEntry.
const (
	AuditSQL   = "sql"
	AuditRedis = "redis"
	AuditKafka = "kafka"
)

// AuditEntry is one write that reached a server.
type AuditEntry struct {
	ID       int64 // set by Audit; ignored by AddAudit
	Time     time.Time
	Conn     string
	Kind     string // AuditSQL, AuditRedis or AuditKafka
	Database string // SQL database, Redis db number; "" for Kafka
	Op       string // "UPDATE", "DEL", "delete_topic", …
	Object   string // table, key, topic, group, subject, connector, principal
	Detail   string
	Duration time.Duration
	Err      string // "" on success
}

// AuditQuery selects entries: Conn and Kind match exactly when set, Text
// is a substring of Op, Object or Detail, BeforeID > 0 keeps entries older
// than that ID (the next page).
type AuditQuery struct {
	Conn, Kind, Text string
	BeforeID         int64
	Limit            int
}

var auditSchema = []string{
	`CREATE TABLE IF NOT EXISTS audit (
		id          INTEGER PRIMARY KEY,
		at          INTEGER NOT NULL,
		conn        TEXT NOT NULL,
		kind        TEXT NOT NULL,
		db_name     TEXT NOT NULL,
		op          TEXT NOT NULL,
		object      TEXT NOT NULL,
		detail      TEXT NOT NULL,
		duration_ms INTEGER NOT NULL,
		error       TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS audit_conn ON audit (conn, id)`,
}

// AddAudit records one write and prunes the oldest entries.
func (s *Store) AddAudit(e AuditEntry) error {
	_, err := s.db.Exec(`INSERT INTO audit (at, conn, kind, db_name, op, object, detail, duration_ms, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Time.UnixMilli(), e.Conn, e.Kind, e.Database, e.Op, e.Object, e.Detail, e.Duration.Milliseconds(), e.Err)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM audit WHERE id <= (SELECT max(id) FROM audit) - ?`, maxAudit)
	return err
}

// Audit returns the entries q selects, newest first.
func (s *Store) Audit(q AuditQuery) ([]AuditEntry, error) {
	var where []string
	var args []any
	if q.Conn != "" {
		where, args = append(where, "conn = ?"), append(args, q.Conn)
	}
	if q.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, q.Kind)
	}
	if q.Text != "" {
		like := "%" + escapeLike(q.Text) + "%"
		where = append(where, `(op LIKE ? ESCAPE '\' OR object LIKE ? ESCAPE '\' OR detail LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	if q.BeforeID > 0 {
		where, args = append(where, "id < ?"), append(args, q.BeforeID)
	}
	query := `SELECT id, at, conn, kind, db_name, op, object, detail, duration_ms, error FROM audit`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := s.db.Query(query+" ORDER BY id DESC LIMIT ?", append(args, q.Limit)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at, ms int64
		if err := rows.Scan(&e.ID, &at, &e.Conn, &e.Kind, &e.Database, &e.Op, &e.Object, &e.Detail, &ms, &e.Err); err != nil {
			return nil, err
		}
		e.Time = time.UnixMilli(at).UTC()
		e.Duration = time.Duration(ms) * time.Millisecond
		out = append(out, e)
	}
	return out, rows.Err()
}
