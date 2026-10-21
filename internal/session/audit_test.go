package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
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
