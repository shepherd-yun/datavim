package session

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// auditDetailMax bounds an audit entry's Detail, in runes.
const auditDetailMax = 2000

// record fills e's Time, Duration (since start) and Err and hands it to
// m.Audit; without an Audit it does nothing.
func (m *Manager) record(e history.AuditEntry, start time.Time, err error) {
	if m.Audit == nil {
		return
	}
	e.Time, e.Duration = start, time.Since(start)
	if err != nil {
		e.Err = err.Error()
	}
	e.Detail = clipDetail(e.Detail)
	m.Audit(e)
}

// clipDetail cuts s to auditDetailMax runes, ending in "…".
func clipDetail(s string) string {
	if len(s) <= auditDetailMax {
		return s
	}
	r := []rune(s)
	if len(r) <= auditDetailMax {
		return s
	}
	return string(r[:auditDetailMax-1]) + "…"
}

// audit records a Kafka write of g's connection.
func (g *guardedKafka) audit(op kmsgx.Op, object, detail string, start time.Time, err error) {
	g.m.record(history.AuditEntry{Conn: g.name, Kind: history.AuditKafka, Op: op.String(), Object: object, Detail: detail}, start, err)
}

// maxAuditParts is how many partitions or configs a Kafka entry lists.
const maxAuditParts = 20

func joinParts(parts []string, sep string) string {
	if len(parts) > maxAuditParts {
		return strings.Join(parts[:maxAuditParts], sep) + fmt.Sprintf("%s… +%d", sep, len(parts)-maxAuditParts)
	}
	return strings.Join(parts, sep)
}

func resetDetail(commits []kafka.OffsetCommit) string {
	parts := make([]string, len(commits))
	for i, c := range commits {
		parts[i] = fmt.Sprintf("%s p%d %d → %d", c.Topic, c.Partition, c.Expect, c.To)
	}
	return joinParts(parts, ", ")
}

func configsDetail(changes []kafka.ConfigChange) string {
	parts := make([]string, len(changes))
	for i, c := range changes {
		if c.Delete {
			parts[i] = c.Name + " → default"
		} else {
			parts[i] = fmt.Sprintf("%s %s → %s", c.Name, c.Old, c.Value)
		}
	}
	return joinParts(parts, "; ")
}

func deleteRecordsDetail(before map[int32]int64) string {
	ps := make([]int32, 0, len(before))
	for p := range before {
		ps = append(ps, p)
	}
	slices.Sort(ps)
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = fmt.Sprintf("p%d before %d", p, before[p])
	}
	return joinParts(parts, ", ")
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}

// readOther are statements of Kind Other that only read or set up the
// session; the audit leaves them out.
var readOther = map[string]bool{"USE": true, "SHOW": true, "DESCRIBE": true, "DESC": true, "EXPLAIN": true}

// auditSQL reports whether sql may write and the entry's Op and Object:
// the first statement in it of Kind Write or DDL, or Other that is not
// SafeOther. Tx statements count only in a Session (inTx).
func auditSQL(sql string, d sqlx.Dialect, inTx bool) (op, object string, ok bool) {
	for _, info := range sqlx.AnalyzeBatch(sql, d) {
		word := strings.ToUpper(sqlx.FirstWord(info.SQL, d))
		switch info.Kind {
		case sqlx.Write, sqlx.DDL:
		case sqlx.Tx:
			if !inTx {
				continue
			}
		case sqlx.Other:
			if word == "" || readOther[word] || sqlx.SafeOther(info.SQL, d) {
				continue
			}
		default:
			continue
		}
		return word, info.Target, true
	}
	return "", "", false
}

// sqlAudit is what auditedConn and auditedSession share.
type sqlAudit struct {
	m              *Manager
	name, database string
	dialect        sqlx.Dialect
	inTx           bool
}

// run calls fn and records sql when it may write.
func (a sqlAudit) run(sql string, fn func() (rows int64, hasRows bool, err error)) error {
	op, object, ok := auditSQL(sql, a.dialect, a.inTx)
	if !ok || a.m.Audit == nil {
		_, _, err := fn()
		return err
	}
	start := time.Now()
	rows, hasRows, err := fn()
	detail := sqlx.RedactSQL(sql, a.dialect)
	if err == nil && hasRows {
		detail = clipDetail(detail) + fmt.Sprintf(" · %d rows", rows)
	}
	a.m.record(history.AuditEntry{
		Conn: a.name, Kind: history.AuditSQL, Database: a.database, Op: op, Object: object, Detail: detail,
	}, start, err)
	return err
}

func (a sqlAudit) query(ctx context.Context, q db.Querier, sql string, args []any) (*db.ResultSet, error) {
	var rs *db.ResultSet
	err := a.run(sql, func() (int64, bool, error) {
		var err error
		rs, err = q.Query(ctx, sql, args...)
		return 0, false, err
	})
	return rs, err
}

func (a sqlAudit) exec(ctx context.Context, q db.Querier, sql string, args []any) (db.ExecResult, error) {
	var res db.ExecResult
	err := a.run(sql, func() (int64, bool, error) {
		var err error
		res, err = q.Exec(ctx, sql, args...)
		return res.RowsAffected, true, err
	})
	return res, err
}

func (a sqlAudit) stream(ctx context.Context, q db.Querier, sql string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	return a.run(sql, func() (int64, bool, error) {
		return 0, false, q.Stream(ctx, sql, onColumns, onRow)
	})
}

// auditedConn records the statements of Query, Exec and Stream that may
// write; Session wraps the session it opens the same way. Meta and Server
// pass through: they only read.
type auditedConn struct {
	db.Conn
	a sqlAudit
}

func (c *auditedConn) Query(ctx context.Context, sql string, args ...any) (*db.ResultSet, error) {
	return c.a.query(ctx, c.Conn, sql, args)
}

func (c *auditedConn) Exec(ctx context.Context, sql string, args ...any) (db.ExecResult, error) {
	return c.a.exec(ctx, c.Conn, sql, args)
}

func (c *auditedConn) Stream(ctx context.Context, sql string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	return c.a.stream(ctx, c.Conn, sql, onColumns, onRow)
}

func (c *auditedConn) Session(ctx context.Context) (db.Session, error) {
	s, err := c.Conn.Session(ctx)
	if err != nil {
		return nil, err
	}
	a := c.a
	a.inTx = true
	return &auditedSession{Session: s, a: a}, nil
}

// auditedSession is a console's session; its BEGIN, COMMIT and ROLLBACK
// are recorded too, so the log shows what a transaction kept.
type auditedSession struct {
	db.Session
	a sqlAudit
}

func (s *auditedSession) Query(ctx context.Context, sql string, args ...any) (*db.ResultSet, error) {
	return s.a.query(ctx, s.Session, sql, args)
}

func (s *auditedSession) Exec(ctx context.Context, sql string, args ...any) (db.ExecResult, error) {
	return s.a.exec(ctx, s.Session, sql, args)
}

func (s *auditedSession) Stream(ctx context.Context, sql string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	return s.a.stream(ctx, s.Session, sql, onColumns, onRow)
}
