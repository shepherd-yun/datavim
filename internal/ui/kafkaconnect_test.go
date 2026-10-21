package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/session"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

func sinkConnector() kafka.Connector {
	return kafka.Connector{
		Name: "orders-sink", Type: "sink", Class: "FileStreamSink", State: "RUNNING", Worker: "w1:8083",
		Tasks: []kafka.ConnectorTask{
			{ID: 0, State: "RUNNING", Worker: "w1:8083"},
			{ID: 1, State: "RUNNING", Worker: "w2:8083"},
			{ID: 2, State: "FAILED", Worker: "w2:8083", Trace: "org.apache.kafka.connect.errors.ConnectException: boom\n\tat Foo.bar(Foo.java:1)"},
		},
		Config: map[string]string{
			"connector.class": "FileStreamSink", "tasks.max": "3", "topics": "orders",
			"connection.password": "s3cret", "file": "/tmp/out",
		},
	}
}

func connectModel(t *testing.T, url string) (*Model, *fakeBackend, *fakeConnect) {
	t.Helper()
	m, b, _ := newKafkaModel(t, config.Connection{Params: map[string]string{"connect_url": url}})
	c := b.addConnect("connect")
	c.connectors = []kafka.Connector{sinkConnector(), {Name: "pg-source", Type: "source", State: "PAUSED", Tasks: []kafka.ConnectorTask{{ID: 0, State: "PAUSED"}}}}
	return m, b, c
}

func expandConnect(t *testing.T, m *Model) *tree.Node {
	t.Helper()
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindKafkaSection && n.Section == secConnect })
	pressAll(t, m, "l")
	return section(t, m, "events", secConnect)
}

func TestConnectTreeOneCluster(t *testing.T) {
	m, _, _ := connectModel(t, "http://c1:8083")
	sec := expandConnect(t, m)
	if sec.Name != "Kafka Connect (2)" {
		t.Fatalf("section = %q", sec.Name)
	}
	got := sec.Children
	if len(got) != 2 || got[0].Kind != tree.KindConnector || got[0].Name != "orders-sink" || got[1].Name != "pg-source" {
		t.Fatalf("children = %v", names(got))
	}
	if got[0].Detail != "RUNNING 2/3" || !got[0].Warn {
		t.Fatalf("orders-sink: %q warn %v", got[0].Detail, got[0].Warn)
	}
	if got[1].Detail != "PAUSED" || got[1].Warn {
		t.Fatalf("pg-source: %q warn %v", got[1].Detail, got[1].Warn)
	}
}

func TestConnectTreeClusters(t *testing.T) {
	m, b, _ := newKafkaModel(t, config.Connection{Params: map[string]string{"connect_url": "main=http://c1:8083, cdc=http://c2:8083"}})
	main := b.addConnect("main")
	main.connectors = []kafka.Connector{sinkConnector()}
	b.addConnect("cdc")
	b.connectErr = map[string]error{"cdc": kafka.ErrConnectAuth}
	sec := expandConnect(t, m)
	cls := sec.Children
	if len(cls) != 2 || cls[0].Kind != tree.KindConnectCluster || cls[0].Name != "main" || cls[1].Name != "cdc" {
		t.Fatalf("clusters = %v", names(cls))
	}
	if cls[0].Detail != "3.7.0 · 1 connector" || len(cls[0].Children) != 1 {
		t.Fatalf("main: %q %v", cls[0].Detail, names(cls[0].Children))
	}
	if !strings.Contains(cls[1].Err, "authentication failed") {
		t.Fatalf("cdc err = %q", cls[1].Err)
	}
}

func TestConnectTreeSecret(t *testing.T) {
	m, b, _ := connectModel(t, "http://c1:8083")
	b.connectErr = map[string]error{"connect": &session.SecretRequiredError{Conn: "events", Kind: session.SecretConnect}}
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	if _, ok := m.overlay.(*overlay.Secret); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	if got := names(m.tree.Roots()[0].Children); len(got) < 4 || got[1] != "Topics (0)" {
		t.Fatalf("the tree waited for connect: %v", got)
	}
}

func openConnector(t *testing.T, m *Model) *connectorTab {
	t.Helper()
	expandConnect(t, m)
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindConnector && n.Name == "orders-sink" })
	pressAll(t, m, "enter")
	ct, ok := m.activeTab().(*connectorTab)
	if !ok {
		t.Fatalf("active tab = %T (%s)", m.activeTab(), m.status)
	}
	return ct
}

func TestConnectorTab(t *testing.T) {
	m, _, _ := connectModel(t, "http://c1:8083")
	ct := openConnector(t, m)
	if ct.Title() != "connect/orders-sink" || ct.Context() != keymap.KafkaConnect {
		t.Fatalf("title %q context %s", ct.Title(), ct.Context())
	}
	v := screen(m)
	for _, w := range []string{"Status", "Tasks", "Config", "RUNNING", "FileStreamSink", "w1:8083", "sink"} {
		if !strings.Contains(v, w) {
			t.Fatalf("missing %q:\n%s", w, v)
		}
	}
	pressAll(t, m, "]")
	if rs := ct.grids[secCTasks].Result(); len(rs.Rows) != 3 || rs.Rows[2][1].Text != "FAILED" ||
		rs.Rows[2][3].Text != "org.apache.kafka.connect.errors.ConnectException: boom" {
		t.Fatalf("tasks = %v", rs.Rows)
	}
	pressAll(t, m, "G", "K")
	d, ok := m.overlay.(*overlay.Detail)
	if !ok || !strings.Contains(flatView(d), "Foo.bar(Foo.java:1)") {
		t.Fatalf("overlay = %T", m.overlay)
	}
	pressAll(t, m, "esc", "]")
	cfg := map[string]string{}
	for _, r := range ct.grids[secCConfig].Result().Rows {
		cfg[r[0].Text] = r[1].Text
	}
	if cfg["connection.password"] != "******" || cfg["tasks.max"] != "3" {
		t.Fatalf("config = %v", cfg)
	}
	if strings.Contains(screen(m), "s3cret") {
		t.Fatal("the password is on screen")
	}
}

func TestConnectorTabRefresh(t *testing.T) {
	m, _, c := connectModel(t, "http://c1:8083")
	ct := openConnector(t, m)
	ct.interval = time.Hour // ticks are sent by hand
	before := len(c.callsOf("Connector "))
	cmd := ct.Update(connectorTickMsg{tabID: ct.id, gen: ct.gen})
	if cmd == nil {
		t.Fatal("no refresh on a tick while shown")
	}
	if next := ct.Update(cmd()); next == nil {
		t.Fatal("no next tick after a refresh")
	}
	if got := len(c.callsOf("Connector ")); got != before+1 {
		t.Fatalf("Connector calls %d → %d", before, got)
	}
	ct.Hidden()
	if cmd := ct.Update(connectorTickMsg{tabID: ct.id, gen: ct.gen}); cmd != nil {
		t.Fatal("refreshes while hidden")
	}
}

func TestSensitiveConnectKey(t *testing.T) {
	for k, want := range map[string]bool{
		"connection.password": true, "sasl.jaas.config": true, "aws.secret.key": true, "api.token": true,
		"database.credentials": true, "ssl.key": true,
		"key.converter": false, "topics": false, "tasks.max": false, "connection.user": false,
	} {
		if got := sensitiveConnectKey(k); got != want {
			t.Errorf("sensitiveConnectKey(%q) = %v", k, got)
		}
	}
}
