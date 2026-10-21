package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
)

func addAudit(t *testing.T, m *Model, es ...history.AuditEntry) *fakeStore {
	t.Helper()
	st := m.store.(*fakeStore)
	for _, e := range es {
		if e.Time.IsZero() {
			e.Time = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
		}
		if err := st.AddAudit(e); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func openAudit(t *testing.T, m *Model) *auditTab {
	t.Helper()
	pressAll(t, m, "space", "a")
	a, ok := m.activeTab().(*auditTab)
	if !ok {
		t.Fatalf("active tab = %T (%s)", m.activeTab(), m.status)
	}
	return a
}

func TestAuditTabOpens(t *testing.T) {
	m, _ := newTestModel(t)
	addAudit(t, m,
		history.AuditEntry{Conn: "local", Kind: history.AuditSQL, Database: "app", Op: "UPDATE", Object: "users", Detail: "UPDATE users SET a = 1 · 1 rows"},
		history.AuditEntry{Conn: "k", Kind: history.AuditKafka, Op: "delete_topic", Object: "orders", Err: "kafka: permission denied\nmore"},
		history.AuditEntry{Conn: "r", Kind: history.AuditRedis, Database: "0", Op: "SET", Object: "k", Detail: "SET k v"},
	)
	a := openAudit(t, m)
	if a.Title() != "audit" || a.Context() != keymap.Audit {
		t.Fatalf("title %q, context %s", a.Title(), a.Context())
	}
	rs := a.grid.Result()
	if len(rs.Rows) != 3 {
		t.Fatalf("rows = %d", len(rs.Rows))
	}
	col := map[string]int{}
	for i, c := range rs.Columns {
		col[c.Name] = i
	}
	for _, name := range []string{"TIME", "CONN", "KIND", "DB", "OP", "OBJECT", "DETAIL", "RESULT", "TOOK"} {
		if _, ok := col[name]; !ok {
			t.Fatalf("no %s column: %v", name, rs.Columns)
		}
	}
	if rs.Rows[0][col["OP"]].Text != "SET" || rs.Rows[2][col["OP"]].Text != "UPDATE" {
		t.Fatalf("not newest first: %v", rs.Rows)
	}
	if got := rs.Rows[1][col["RESULT"]].Text; got != "kafka: permission denied" {
		t.Fatalf("failed RESULT = %q", got)
	}
	if got := rs.Rows[0][col["RESULT"]].Text; got != "ok" {
		t.Fatalf("RESULT = %q", got)
	}
}

func TestAuditTabConn(t *testing.T) {
	m, _ := newTestModel(t)
	st := addAudit(t, m, history.AuditEntry{Conn: "local", Kind: history.AuditSQL, Op: "UPDATE"})
	press(t, m, ":")
	typeText(t, m, "audit local")
	press(t, m, "enter")
	a, ok := m.activeTab().(*auditTab)
	if !ok || a.Title() != "audit · local" {
		t.Fatalf("active tab = %T (%s)", m.activeTab(), m.status)
	}
	if q := st.lastAuditQuery(); q.Conn != "local" {
		t.Fatalf("query = %+v", q)
	}
	press(t, m, ":")
	typeText(t, m, "audit nope")
	press(t, m, "enter")
	if !strings.Contains(m.status, `unknown connection "nope"`) {
		t.Fatalf("status = %q", m.status)
	}
}

func TestAuditTabFilter(t *testing.T) {
	m, _ := newTestModel(t)
	st := addAudit(t, m,
		history.AuditEntry{Conn: "k", Kind: history.AuditKafka, Op: "produce", Object: "orders"},
		history.AuditEntry{Conn: "k", Kind: history.AuditKafka, Op: "produce", Object: "refunds"},
	)
	a := openAudit(t, m)
	press(t, m, "F")
	typeText(t, m, "orders")
	press(t, m, "enter")
	if q := st.lastAuditQuery(); q.Text != "orders" {
		t.Fatalf("query = %+v", q)
	}
	if n := len(a.grid.Result().Rows); n != 1 {
		t.Fatalf("rows = %d", n)
	}
	press(t, m, "f", "x")
	if q := st.lastAuditQuery(); q.Text != "" || len(a.grid.Result().Rows) != 2 {
		t.Fatalf("clear: query %+v, rows %d", q, len(a.grid.Result().Rows))
	}
}

func TestAuditTabGoto(t *testing.T) {
	m, b, k := newKafkaModel(t, config.Connection{})
	b.conns = append(b.conns, config.Connection{Name: "local", Driver: "mysql"})
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "g1"}})
	addAudit(t, m,
		history.AuditEntry{Conn: "local", Kind: history.AuditSQL, Database: "app", Op: "UPDATE", Object: "users"},
		history.AuditEntry{Conn: "events", Kind: history.AuditKafka, Op: "delete_group", Object: "g1"},
	)
	openAudit(t, m)
	press(t, m, "g", "d")
	if g, ok := m.activeTab().(*groupTab); !ok || g.group != "g1" {
		t.Fatalf("active tab = %T (%s)", m.activeTab(), m.status)
	}
	openAudit(t, m)
	press(t, m, "j", "g", "d")
	if d, ok := m.activeTab().(*dataTab); !ok || d.ref.Name != "users" || d.ref.Database != "app" {
		t.Fatalf("active tab = %T (%s)", m.activeTab(), m.status)
	}
}

func TestAuditTabPaging(t *testing.T) {
	m, _ := newTestModel(t)
	var es []history.AuditEntry
	for i := range 1500 {
		es = append(es, history.AuditEntry{Conn: "k", Kind: history.AuditKafka, Op: "produce", Detail: fmt.Sprint(i)})
	}
	st := addAudit(t, m, es...)
	a := openAudit(t, m)
	if n := len(a.grid.Result().Rows); n != auditPage {
		t.Fatalf("first page = %d rows", n)
	}
	press(t, m, "G", "j")
	if q := st.lastAuditQuery(); q.BeforeID != 501 {
		t.Fatalf("next page query = %+v", q)
	}
	if n := len(a.grid.Result().Rows); n != 1500 {
		t.Fatalf("rows = %d", n)
	}
}

func TestAuditHint(t *testing.T) {
	m, _ := newTestModel(t)
	if h := keyHint(m.keys, keymap.Global, keymap.AuditLog, "audit"); h == "" {
		t.Fatal("AuditLog has no key")
	}
	if d := keymap.KnownActions()[keymap.AuditLog]; d != "Audit log of writes" {
		t.Fatalf("description = %q", d)
	}
}

func TestAuditTabGotoConnector(t *testing.T) {
	m, _, _ := connectModel(t, "http://c1:8083")
	addAudit(t, m,
		history.AuditEntry{Conn: "events", Kind: history.AuditKafka, Op: "create_acl", Object: "User:alice"},
		history.AuditEntry{Conn: "events", Kind: history.AuditKafka, Op: "pause_connector", Object: "connect/orders-sink"},
	)
	openAudit(t, m)
	press(t, m, "g", "d")
	if c, ok := m.activeTab().(*connectorTab); !ok || c.name != "orders-sink" || c.cluster != "connect" {
		t.Fatalf("active tab = %T (%s)", m.activeTab(), m.status)
	}
	openAudit(t, m)
	press(t, m, "j", "g", "d")
	if _, ok := m.activeTab().(*aclTab); !ok {
		t.Fatalf("active tab = %T (%s)", m.activeTab(), m.status)
	}
}
