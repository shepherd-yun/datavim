package history

import (
	"testing"
	"time"
)

func addAudits(t *testing.T, s *Store, es ...AuditEntry) {
	t.Helper()
	for _, e := range es {
		if err := s.AddAudit(e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAudit(t *testing.T) {
	s, _ := open(t)
	base := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	addAudits(t, s,
		AuditEntry{
			Time: base, Conn: "pg", Kind: AuditSQL, Database: "app", Op: "UPDATE", Object: "users",
			Detail: "UPDATE users SET a = '50%' WHERE id = 1 · 1 rows", Duration: 3 * time.Millisecond,
		},
		AuditEntry{Time: base.Add(time.Minute), Conn: "k", Kind: AuditKafka, Op: "delete_topic", Object: "orders_v1", Err: "kafka: permission denied"},
		AuditEntry{Time: base.Add(2 * time.Minute), Conn: "k", Kind: AuditKafka, Op: "produce", Object: "orders", Detail: "3 messages"},
	)
	all, err := s.Audit(AuditQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Op != "produce" || all[2].Op != "UPDATE" {
		t.Fatalf("order: %+v", all)
	}
	got := all[2]
	if got.ID == 0 || !got.Time.Equal(base) || got.Conn != "pg" || got.Database != "app" || got.Object != "users" ||
		got.Duration != 3*time.Millisecond || got.Kind != AuditSQL {
		t.Fatalf("round trip: %+v", got)
	}
	if all[1].Err != "kafka: permission denied" {
		t.Fatalf("err: %+v", all[1])
	}
	for _, tc := range []struct {
		q    AuditQuery
		want []string
	}{
		{AuditQuery{Conn: "k", Limit: 10}, []string{"produce", "delete_topic"}},
		{AuditQuery{Kind: AuditSQL, Limit: 10}, []string{"UPDATE"}},
		{AuditQuery{Text: "orders", Limit: 10}, []string{"produce", "delete_topic"}},
		{AuditQuery{Text: "50%", Limit: 10}, []string{"UPDATE"}},
		{AuditQuery{Text: "orders_", Limit: 10}, []string{"delete_topic"}},
		{AuditQuery{Text: "messages", Limit: 10}, []string{"produce"}},
		{AuditQuery{BeforeID: all[1].ID, Limit: 10}, []string{"UPDATE"}},
		{AuditQuery{Limit: 1}, []string{"produce"}},
	} {
		es, err := s.Audit(tc.q)
		if err != nil {
			t.Fatal(err)
		}
		var ops []string
		for _, e := range es {
			ops = append(ops, e.Op)
		}
		if len(ops) != len(tc.want) {
			t.Errorf("%+v: %v, want %v", tc.q, ops, tc.want)
			continue
		}
		for i := range ops {
			if ops[i] != tc.want[i] {
				t.Errorf("%+v: %v, want %v", tc.q, ops, tc.want)
			}
		}
	}
}

func TestAuditPrune(t *testing.T) {
	old := maxAudit
	maxAudit = 5
	t.Cleanup(func() { maxAudit = old })
	s, _ := open(t)
	for i := range 7 {
		addAudits(t, s, AuditEntry{Time: time.Now(), Conn: "k", Kind: AuditKafka, Op: "produce", Detail: string(rune('a' + i))})
	}
	es, err := s.Audit(AuditQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 5 || es[0].Detail != "g" || es[4].Detail != "c" {
		t.Fatalf("pruned: %+v", es)
	}
}
