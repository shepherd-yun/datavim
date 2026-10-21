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
	"github.com/shepherd-yun/sqlvim/internal/rcmd"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/redit"
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

// redisWriteMethods are the rdb.Client methods that change the server;
// auditedRedis records each. A test fails when rdb.Client gains a method
// in neither this list nor the test's read list.
var redisWriteMethods = []string{"Apply", "Expire", "Rename", "Unlink", "KillClient"}

// auditedRedis records the writes of a Redis client of db n.
type auditedRedis struct {
	rdb.Client
	m    *Manager
	name string
	n    int
}

func (c *auditedRedis) record(op, object, detail string, start time.Time, err error) {
	c.m.record(history.AuditEntry{
		Conn: c.name, Kind: history.AuditRedis, Database: strconv.Itoa(c.n), Op: op, Object: object, Detail: detail,
	}, start, err)
}

func (c *auditedRedis) Apply(ctx context.Context, p redit.Plan) error {
	start := time.Now()
	err := c.Client.Apply(ctx, p)
	cmds := make([]string, len(p.Cmds))
	for i, cmd := range p.Cmds {
		cmds[i] = rcmd.Redact(cmd)
	}
	c.record("apply", p.Key, strings.Join(cmds, "; "), start, err)
	return err
}

func (c *auditedRedis) Expire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	start := time.Now()
	ok, err := c.Client.Expire(ctx, key, ttl)
	if ttl < 0 {
		c.record("PERSIST", key, "", start, err)
	} else {
		c.record("EXPIRE", key, ttl.String(), start, err)
	}
	return ok, err
}

func (c *auditedRedis) Rename(ctx context.Context, from, to string, overwrite bool) (bool, error) {
	start := time.Now()
	ok, err := c.Client.Rename(ctx, from, to, overwrite)
	detail := "→ " + to
	if overwrite {
		detail += " (overwrite)"
	}
	c.record("RENAME", from, detail, start, err)
	return ok, err
}

func (c *auditedRedis) Unlink(ctx context.Context, keys ...string) (int64, error) {
	start := time.Now()
	n, err := c.Client.Unlink(ctx, keys...)
	first := ""
	if len(keys) > 0 {
		first = keys[0]
	}
	c.record("UNLINK", first, plural(len(keys), "key"), start, err)
	return n, err
}

func (c *auditedRedis) KillClient(ctx context.Context, id int64) (bool, error) {
	start := time.Now()
	ok, err := c.Client.KillClient(ctx, id)
	c.record("CLIENT KILL", strconv.FormatInt(id, 10), "", start, err)
	return ok, err
}

// auditedRedisSession records a console's commands that may write
// (rcmd's Write, Dangerous and Unknown) and MULTI / EXEC / DISCARD.
type auditedRedisSession struct {
	rdb.Session
	m    *Manager
	name string
}

var redisTxCommands = map[string]bool{"MULTI": true, "EXEC": true, "DISCARD": true}

func (s *auditedRedisSession) Do(ctx context.Context, args ...string) (rdb.Reply, error) {
	level, name := rcmd.Classify(args)
	if (level == rcmd.Read && !redisTxCommands[name]) || level == rcmd.Unsupported || s.m.Audit == nil {
		return s.Session.Do(ctx, args...)
	}
	start := time.Now()
	db := s.DB()
	reply, err := s.Session.Do(ctx, args...)
	object := "" // the first argument after the command (and subcommand) name
	if i := strings.Count(name, " ") + 1; i < len(args) {
		object = args[i]
	}
	s.m.record(history.AuditEntry{
		Conn: s.name, Kind: history.AuditRedis, Database: strconv.Itoa(db), Op: name, Object: object, Detail: rcmd.Redact(args),
	}, start, err)
	return reply, err
}
