package session

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/redit"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// auditLog collects what a Manager audits.
type auditLog struct {
	mu sync.Mutex
	es []history.AuditEntry
}

func recordAudit(m *Manager) *auditLog {
	l := &auditLog{}
	m.Audit = func(e history.AuditEntry) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.es = append(l.es, e)
	}
	return l
}

func (l *auditLog) entries() []history.AuditEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]history.AuditEntry(nil), l.es...)
}

// checkOnePerWrite: every write method was audited once, in order, each
// with its own op name.
func (l *auditLog) checkOnePerWrite(t *testing.T, methods []string) {
	t.Helper()
	es := l.entries()
	if len(es) != len(methods) {
		t.Fatalf("%d audit entries for %d writes: %+v", len(es), len(methods), es)
	}
	ops := map[string]bool{}
	for i, e := range es {
		if e.Kind != history.AuditKafka || e.Conn != "k" || e.Op == "" || e.Time.IsZero() {
			t.Errorf("%s: entry %+v", methods[i], e)
		}
		if ops[e.Op] {
			t.Errorf("%s: op %q twice", methods[i], e.Op)
		}
		ops[e.Op] = true
	}
}

func TestAuditKafkaFailure(t *testing.T) {
	m, op, _ := setupKafka(config.Connection{})
	audit := recordAudit(m)
	c, err := m.Kafka(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	op.clients[0].err = kafka.ErrDenied
	if err := c.DeleteTopic(context.Background(), "orders"); !errors.Is(err, kafka.ErrDenied) {
		t.Fatalf("err = %v", err)
	}
	es := audit.entries()
	if len(es) != 1 || es[0].Op != "delete_topic" || es[0].Object != "orders" || !strings.Contains(es[0].Err, "permission denied") {
		t.Fatalf("audit = %+v", es)
	}
}

func TestAuditNil(t *testing.T) {
	m, _, _ := setupKafka(config.Connection{})
	c, err := m.Kafka(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteTopic(context.Background(), "orders"); err != nil {
		t.Fatal(err)
	}
}

func TestAuditKafkaDetail(t *testing.T) {
	m, _, _ := setupKafka(config.Connection{})
	audit := recordAudit(m)
	c, err := m.Kafka(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, _ = c.Produce(ctx, "orders", make([]kafka.ProduceRecord, 3))
	_ = c.CreateTopic(ctx, kafka.NewTopic{Name: "t2", Partitions: 12, Replication: 3})
	_, _ = c.ResetOffsets(ctx, "g1", []kafka.OffsetCommit{{TP: kafka.TP{Topic: "orders", Partition: 0}, Expect: 120, To: 80}})
	_ = c.AlterTopicConfigs(ctx, "orders", []kafka.ConfigChange{
		{Name: "retention.ms", Value: "86400000", Old: "604800000"}, {Name: "cleanup.policy", Delete: true, Old: "compact"},
	})
	_ = c.AddPartitions(ctx, "orders", 12)
	_, _ = c.DeleteRecords(ctx, "orders", map[int32]int64{0: 100})
	_, _ = c.DeleteGroupOffsets(ctx, "g1", "orders")
	want := []struct{ op, object, detail string }{
		{"produce", "orders", "3 messages"},
		{"create_topic", "t2", "12 partitions · 3 replicas"},
		{"reset_offsets", "g1", "orders p0 120 → 80"},
		{"alter_configs", "orders", "retention.ms 604800000 → 86400000; cleanup.policy → default"},
		{"add_partitions", "orders", "→ 12 partitions"},
		{"delete_records", "orders", "p0 before 100"},
		{"delete_offsets", "g1", "topic orders"},
	}
	es := audit.entries()
	if len(es) != len(want) {
		t.Fatalf("audit = %+v", es)
	}
	for i, w := range want {
		if e := es[i]; e.Op != w.op || e.Object != w.object || e.Detail != w.detail {
			t.Errorf("entry %d = %q %q %q, want %q %q %q", i, e.Op, e.Object, e.Detail, w.op, w.object, w.detail)
		}
	}
}

func TestClipDetail(t *testing.T) {
	long := strings.Repeat("é", auditDetailMax+1)
	got := clipDetail(long)
	if r := []rune(got); len(r) != auditDetailMax || r[len(r)-1] != '…' {
		t.Fatalf("clipped to %d runes", len(r))
	}
	if clipDetail("short") != "short" {
		t.Fatal("short text changed")
	}
}

func TestAuditSQLKinds(t *testing.T) {
	for _, tc := range []struct {
		sql        string
		d          sqlx.Dialect
		inTx       bool
		op, object string
		ok         bool
	}{
		{"SELECT 1", sqlx.Postgres, false, "", "", false},
		{"UPDATE t SET a=1 WHERE id=2", sqlx.Postgres, false, "UPDATE", "t", true},
		{"create table x(id int)", sqlx.MySQL, false, "CREATE", "", true},
		{"SELECT 1; DELETE FROM t", sqlx.Postgres, false, "DELETE", "t", true},
		{"COMMIT", sqlx.Postgres, true, "COMMIT", "", true},
		{"COMMIT", sqlx.Postgres, false, "", "", false},
		{"EXEC sp_help", sqlx.SQLServer, false, "", "", false},
		{"EXEC dbo.purge", sqlx.SQLServer, false, "EXEC", "", true},
		{"SHOW TABLES", sqlx.MySQL, false, "", "", false},
		{"USE app", sqlx.MySQL, false, "", "", false},
	} {
		op, object, ok := auditSQL(tc.sql, tc.d, tc.inTx)
		if ok != tc.ok || op != tc.op || (tc.object != "" && object != tc.object) {
			t.Errorf("auditSQL(%q, inTx=%v) = %q %q %v, want %q %q %v", tc.sql, tc.inTx, op, object, ok, tc.op, tc.object, tc.ok)
		}
	}
}

// sqlConn is a db.Conn whose Querier calls succeed (or fail with err).
type sqlConn struct {
	fakeConn
	err error
}

func (c *sqlConn) Exec(context.Context, string, ...any) (db.ExecResult, error) {
	return db.ExecResult{RowsAffected: 1}, c.err
}

func (c *sqlConn) Query(context.Context, string, ...any) (*db.ResultSet, error) {
	return &db.ResultSet{}, c.err
}

func (c *sqlConn) Stream(context.Context, string, func([]db.ColumnInfo), func([]db.Value) error) error {
	return c.err
}

func (c *sqlConn) Session(context.Context) (db.Session, error) { return &sqlSession{conn: c}, nil }

type sqlSession struct {
	db.Session
	conn *sqlConn
}

func (s *sqlSession) Exec(ctx context.Context, q string, args ...any) (db.ExecResult, error) {
	return s.conn.Exec(ctx, q, args...)
}

func setupSQL(t *testing.T) (*Manager, *sqlConn, *auditLog) {
	t.Helper()
	m, drv, sec := setup(true)
	sec.m["local"] = "pw"
	c := &sqlConn{fakeConn: fakeConn{done: make(chan struct{})}}
	drv.conn = c
	return m, c, recordAudit(m)
}

func TestAuditSQLExec(t *testing.T) {
	m, _, audit := setupSQL(t)
	ctx := context.Background()
	c, err := m.Conn(ctx, "local", "db1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, "UPDATE t SET pw='x' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(ctx, "SELECT * FROM t WHERE id = $1", 7); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, "ALTER USER bob IDENTIFIED BY 'tiger'"); err != nil {
		t.Fatal(err)
	}
	es := audit.entries()
	if len(es) != 2 {
		t.Fatalf("audit = %+v", es)
	}
	if e := es[0]; e.Kind != history.AuditSQL || e.Conn != "local" || e.Database != "db1" || e.Op != "UPDATE" ||
		e.Object != "t" || !strings.HasSuffix(e.Detail, " · 1 rows") {
		t.Fatalf("entry = %+v", e)
	}
	if strings.Contains(es[1].Detail, "tiger") {
		t.Fatalf("password in the audit: %q", es[1].Detail)
	}
}

func TestAuditSQLSession(t *testing.T) {
	m, _, audit := setupSQL(t)
	ctx := context.Background()
	c, err := m.Conn(ctx, "local", "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"BEGIN", "UPDATE t SET a = 1 WHERE id = 1", "SELECT 1", "ROLLBACK"} {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	var ops []string
	for _, e := range audit.entries() {
		ops = append(ops, e.Op)
	}
	if strings.Join(ops, ",") != "BEGIN,UPDATE,ROLLBACK" {
		t.Fatalf("ops = %v", ops)
	}
}

func TestAuditSQLError(t *testing.T) {
	m, raw, audit := setupSQL(t)
	raw.err = errors.New("permission denied for table t")
	ctx := context.Background()
	c, err := m.Conn(ctx, "local", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, "DELETE FROM t WHERE id = 1"); !errors.Is(err, raw.err) {
		t.Fatalf("err = %v", err)
	}
	if es := audit.entries(); len(es) != 1 || es[0].Err != "permission denied for table t" || strings.Contains(es[0].Detail, "rows") {
		t.Fatalf("audit = %+v", es)
	}
}

func TestAuditSQLStream(t *testing.T) {
	m, _, audit := setupSQL(t)
	ctx := context.Background()
	c, err := m.Conn(ctx, "local", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Stream(ctx, "SELECT * FROM t", nil, nil)
	_ = c.Stream(ctx, "INSERT INTO t (a) VALUES (1) RETURNING id", nil, nil)
	if es := audit.entries(); len(es) != 1 || es[0].Op != "INSERT" {
		t.Fatalf("audit = %+v", es)
	}
}

// redisReadMethods are the rdb.Client methods that never change the
// server. Every method must be here or in redisWriteMethods.
var redisReadMethods = []string{
	"Version", "DBSize", "Databases", "Scan", "KeyInfo", "GetString", "HashPage", "ListPage", "SetPage",
	"ZSetPage", "StreamPage", "ScanKeys", "Info", "SlowLog", "Clients", "KeySizes", "Close",
}

func TestRedisMethodsListed(t *testing.T) {
	typ := reflect.TypeFor[rdb.Client]()
	for i := range typ.NumMethod() {
		name := typ.Method(i).Name
		if !slices.Contains(redisReadMethods, name) && !slices.Contains(redisWriteMethods, name) {
			t.Errorf("rdb.Client.%s is neither a read nor a write method: add it to redisWriteMethods (and audit it) or to redisReadMethods", name)
		}
	}
}

func TestAuditRedisWrites(t *testing.T) {
	m, op, _ := setupRedis(config.Connection{})
	audit := recordAudit(m)
	ctx := context.Background()
	c, err := m.Redis(ctx, "r", 3)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Apply(ctx, redit.Plan{Key: "user:1", Cmds: [][]string{{"HSET", "user:1", "name", "a"}, {"AUTH", "pw"}}})
	_, _ = c.Expire(ctx, "user:1", time.Minute)
	_, _ = c.Rename(ctx, "user:1", "user:2", true)
	_, _ = c.Unlink(ctx, "a", "b", "c")
	_, _ = c.KillClient(ctx, 42)
	_, _ = c.KeyInfo(ctx, "user:1")
	if calls := op.clients[0].calls; len(calls) != len(redisWriteMethods)+1 {
		t.Fatalf("calls = %v", calls)
	}
	want := []struct{ op, object, detail string }{
		{"apply", "user:1", "HSET user:1 name a; AUTH ***"},
		{"EXPIRE", "user:1", "1m0s"},
		{"RENAME", "user:1", "→ user:2 (overwrite)"},
		{"UNLINK", "a", "3 keys"},
		{"CLIENT KILL", "42", ""},
	}
	es := audit.entries()
	if len(es) != len(want) {
		t.Fatalf("audit = %+v", es)
	}
	for i, w := range want {
		e := es[i]
		if e.Kind != history.AuditRedis || e.Database != "3" || e.Op != w.op || e.Object != w.object || e.Detail != w.detail {
			t.Errorf("entry %d = %+v, want %q %q %q", i, e, w.op, w.object, w.detail)
		}
	}
}

func TestAuditRedisDo(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		op, object string
		ok         bool
	}{
		{[]string{"GET", "k"}, "", "", false},
		{[]string{"SET", "k", "v"}, "SET", "k", true},
		{[]string{"AUTH", "pw"}, "", "", false},
		{[]string{"CONFIG", "SET", "requirepass", "s3cret"}, "CONFIG SET", "", true},
		{[]string{"FLUSHDB"}, "FLUSHDB", "", true},
		{[]string{"MULTI"}, "MULTI", "", true},
		{[]string{"FROBNICATE", "x"}, "FROBNICATE", "x", true},
	} {
		m, _ := setupRedisSession(config.Connection{})
		audit := recordAudit(m)
		s, err := m.RedisSession(context.Background(), "r", 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = s.Do(context.Background(), tc.args...)
		es := audit.entries()
		if !tc.ok {
			if len(es) != 0 {
				t.Errorf("%v: audited %+v", tc.args, es)
			}
			continue
		}
		if len(es) != 1 || es[0].Op != tc.op || (tc.object != "" && es[0].Object != tc.object) {
			t.Errorf("%v: audit = %+v", tc.args, es)
			continue
		}
		if strings.Contains(es[0].Detail, "s3cret") || strings.Contains(es[0].Detail, "pw") {
			t.Errorf("%v: secret in %q", tc.args, es[0].Detail)
		}
	}
}
