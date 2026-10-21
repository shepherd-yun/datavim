package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/session"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

// newKafkaModel is a model whose only connection is the kafka "events".
func newKafkaModel(t *testing.T, cfg config.Connection) (*Model, *fakeBackend, *fakeKafka) {
	t.Helper()
	b := newFakeBackend()
	b.conns = nil
	if cfg.Name == "" {
		cfg.Name = "events"
	}
	k := b.addKafka(cfg)
	return newTestModelWith(t, b), b, k
}

func names(nodes []*tree.Node) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.Name)
	}
	return out
}

func TestBuildTopicNodes(t *testing.T) {
	topics := []kafka.Topic{
		{Name: "__consumer_offsets", Partitions: 50, Internal: true},
		{Name: "_schemas", Partitions: 1},
		{Name: "orders.created", Partitions: 12},
		{Name: "orders.paid", Partitions: 12, UnderReplicated: 1},
		{Name: "refunds", Partitions: 3},
		{Name: "users-v1", Partitions: 1},
	}
	nodes := buildTopicNodes("events", topics, false)
	if got := names(nodes); !slices.Equal(got, []string{"orders", "refunds", "users-v1"}) {
		t.Fatalf("names = %v", got)
	}
	f := nodes[0]
	if f.Kind != tree.KindTopicFolder || f.Detail != "2" || len(f.Children) != 2 || !f.Loaded {
		t.Fatalf("folder = %+v", f)
	}
	created, paid := f.Children[0], f.Children[1]
	if created.Kind != tree.KindTopic || created.Key != "orders.created" || created.Name != "orders.created" || created.Detail != "12p" || created.Warn {
		t.Fatalf("created = %+v", created)
	}
	if !paid.Warn || paid.Detail != "12p · under-replicated" {
		t.Fatalf("paid = %+v", paid)
	}
	if r := nodes[1]; r.Kind != tree.KindTopic || r.Detail != "3p" {
		t.Fatalf("refunds = %+v", r)
	}
	all := buildTopicNodes("events", topics, true)
	if got := names(all); !slices.Equal(got, []string{"orders", "__consumer_offsets", "_schemas", "refunds", "users-v1"}) {
		t.Fatalf("with internal = %v", got)
	}
}

func TestBuildGroupNodes(t *testing.T) {
	groups := []kafka.Group{
		{Name: "billing", State: "Stable", Type: kafka.GroupClassic},
		{Name: "notify", State: "Empty", Type: kafka.GroupClassic},
		{Name: "order-svc", State: "Stable", Type: kafka.GroupConsumer},
		{Name: "pending", State: "Stable", Type: kafka.GroupClassic},
		{Name: "share-1", State: "Stable", Type: kafka.GroupShare},
	}
	lag := map[string]int64{"billing": 0, "notify": 12, "order-svc": 48188}
	nodes := buildGroupNodes("events", groups, lag, false)
	if got := names(nodes); !slices.Equal(got, []string{"order-svc", "notify", "billing", "pending", "share-1"}) {
		t.Fatalf("by lag = %v", got)
	}
	if n := nodes[0]; n.Detail != "lag 48,188 · consumer" || !n.Warn || n.Kind != tree.KindKafkaGroup || n.Key != "order-svc" {
		t.Fatalf("order-svc = %+v", n)
	}
	if n := nodes[1]; n.Detail != "lag 12 · Empty" || !n.Warn {
		t.Fatalf("notify = %+v", n)
	}
	if n := nodes[2]; n.Detail != "lag 0" || n.Warn {
		t.Fatalf("billing = %+v", n)
	}
	if n := nodes[3]; n.Detail != "" {
		t.Fatalf("pending (lag not known yet) = %+v", n)
	}
	if n := nodes[4]; n.Detail != "share" {
		t.Fatalf("share = %+v", n)
	}
	byName := buildGroupNodes("events", groups, lag, true)
	if got := names(byName); !slices.Equal(got, []string{"billing", "notify", "order-svc", "pending", "share-1"}) {
		t.Fatalf("by name = %v", got)
	}
}

// section returns the section node of conn's tree.
func section(t *testing.T, m *Model, conn, name string) *tree.Node {
	t.Helper()
	for _, r := range m.tree.Roots() {
		if r.Conn != conn {
			continue
		}
		for _, c := range r.Children {
			if c.Kind == tree.KindKafkaSection && c.Section == name {
				return c
			}
		}
	}
	t.Fatalf("no %s section under %s", name, conn)
	return nil
}

// selectNode moves the tree cursor to the first visible node match accepts.
func selectKafkaNode(t *testing.T, m *Model, match func(*tree.Node) bool) {
	t.Helper()
	if !m.tree.SelectWhere(match) {
		t.Fatal("node not visible")
	}
}

func TestKafkaTreeExpand(t *testing.T) {
	m, b, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 10)
	for i := range 150 {
		k.addGroup(kafka.GroupDetail{
			Group: kafka.Group{Name: fmt.Sprintf("g-%03d", i)},
			Lag:   []kafka.PartitionLag{{TP: kafka.TP{Topic: "orders"}, Lag: int64(i)}},
		})
	}
	pressAll(t, m, "l")
	root := m.tree.Roots()[0]
	if got := names(root.Children); !slices.Equal(got, []string{"Brokers (2)", "Topics (1)", "Consumer Groups (150)", "ACLs"}) {
		t.Fatalf("sections = %v", got)
	}
	if len(k.lagCalls) != 2 || len(k.lagCalls[0]) != 100 || len(k.lagCalls[1]) != 50 {
		t.Fatalf("GroupsLag calls: %d", len(k.lagCalls))
	}
	groups := section(t, m, "events", "groups")
	if first := groups.Children[0]; first.Key != "g-149" || first.Detail != "lag 149" {
		t.Fatalf("first group = %+v", first)
	}
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindKafkaSection && n.Section == "groups" })
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Key == "g-042" })
	pressAll(t, m, "space", "r")
	if n := m.tree.Selected(); n.Kind != tree.KindKafkaGroup || n.Key != "g-042" {
		t.Fatalf("after refresh the cursor is on %+v", n)
	}
	if len(k.lagCalls) != 4 {
		t.Fatalf("refresh computes lag again: %d calls", len(k.lagCalls))
	}
	if !section(t, m, "events", "groups").Expanded {
		t.Fatal("refresh keeps the groups open")
	}
	brokers := section(t, m, "events", "brokers")
	if len(brokers.Children) != 2 || brokers.Children[0].Name != "1  b1:9092" || brokers.Children[0].Detail != "rack a · ★ controller" {
		t.Fatalf("brokers = %+v", brokers.Children[0])
	}
	if b.connCalls != 0 {
		t.Fatal("a kafka connection never opens a SQL connection")
	}
}

func TestKafkaTreeFilter(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 0)
	k.addTopic("refunds", 1, 0, 0)
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "order-svc"}})
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Section == "topics" })
	pressAll(t, m, "F")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, "REF")
	pressAll(t, m, "enter")
	topics := section(t, m, "events", "topics")
	if got := names(topics.Children); !slices.Equal(got, []string{"refunds"}) || !strings.Contains(topics.Detail, "filter REF") {
		t.Fatalf("filtered = %v, detail %q", got, topics.Detail)
	}
	if len(section(t, m, "events", "groups").Children) != 1 {
		t.Fatal("the topic filter leaves groups alone")
	}
	pressAll(t, m, "f", "x")
	if got := names(section(t, m, "events", "topics").Children); len(got) != 2 {
		t.Fatalf("cleared = %v", got)
	}
}

func TestKafkaToggleInternal(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 0)
	k.addTopic("__consumer_offsets", 1, 0, 0)
	pressAll(t, m, "l")
	if got := names(section(t, m, "events", "topics").Children); !slices.Equal(got, []string{"orders"}) {
		t.Fatalf("topics = %v", got)
	}
	pressAll(t, m, ".")
	if got := names(section(t, m, "events", "topics").Children); len(got) != 2 {
		t.Fatalf("with internal = %v", got)
	}
	m2, _, k2 := newKafkaModel(t, config.Connection{Params: map[string]string{"show_internal": "true"}})
	k2.addTopic("__consumer_offsets", 1, 0, 0)
	pressAll(t, m2, "l")
	if got := names(section(t, m2, "events", "topics").Children); len(got) != 1 {
		t.Fatalf("show_internal = %v", got)
	}
}

func TestKafkaSortGroups(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "a"}})
	k.addGroup(kafka.GroupDetail{Group: kafka.Group{Name: "b"}, Lag: []kafka.PartitionLag{{Lag: 5}}})
	pressAll(t, m, "l")
	if got := names(section(t, m, "events", "groups").Children); !slices.Equal(got, []string{"b", "a"}) {
		t.Fatalf("by lag = %v", got)
	}
	pressAll(t, m, "s")
	if got := names(section(t, m, "events", "groups").Children); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("by name = %v", got)
	}
}

func TestKafkaSQLActionsRefused(t *testing.T) {
	m, b, _ := newKafkaModel(t, config.Connection{})
	m.setFocus(focusTree)
	pressAll(t, m, "space", "s")
	if !strings.Contains(m.status, "not supported for kafka") {
		t.Fatalf("status = %q", m.status)
	}
	m.status = ""
	pressAll(t, m, "C")
	if !strings.Contains(m.status, "not supported for kafka") {
		t.Fatalf("status = %q", m.status)
	}
	if b.connCalls != 0 {
		t.Fatal("no SQL connection")
	}
	if m.connKind() != keymapKindKafka() {
		t.Fatalf("kind = %v", m.connKind())
	}
}

func TestKafkaPasswordPrompt(t *testing.T) {
	m, b, _ := newKafkaModel(t, config.Connection{User: "app", Params: map[string]string{"sasl": "plain"}})
	b.needPassword = true
	pressAll(t, m, "l")
	if _, ok := m.overlay.(*overlay.Password); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, "pw")
	pressAll(t, m, "enter")
	if root := m.tree.Roots()[0]; len(root.Children) != 4 {
		t.Fatalf("after the password: %+v", root.Children)
	}
}

func TestSecretPromptRetries(t *testing.T) {
	m, b, _ := newKafkaModel(t, config.Connection{Params: map[string]string{
		"sasl": "oauth", "oauth_token_url": "https://idp/token", "oauth_client_id": "cid",
	}})
	b.setConnErr(&session.SecretRequiredError{Conn: "events", Kind: session.SecretOAuth, Wrong: true})
	pressAll(t, m, "l")
	sec, ok := m.overlay.(*overlay.Secret)
	if !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	if v := sec.View(80, 20); !strings.Contains(v, "OAuth") || !strings.Contains(v, "Wrong. Client secret for cid:") {
		t.Fatalf("prompt:\n%s", v)
	}
	typeText(t, m, "s")
	pressAll(t, m, "enter")
	if !slices.Equal(b.secrets, []string{"events:oauth:s"}) {
		t.Fatalf("SetSecret calls = %v", b.secrets)
	}
	if root := m.tree.Roots()[0]; len(root.Children) != 4 {
		t.Fatalf("after the secret: %+v", root.Children)
	}
}

func keymapKindKafka() keymap.Kind { return keymap.KindKafka }

// registryTree is a kafka model with a registry: orders-value v1..v2,
// payments-value v1.
func registryTree(t *testing.T) (*Model, *fakeBackend, *fakeRegistry) {
	t.Helper()
	m, b, k := newKafkaModel(t, withRegistry(config.Connection{}))
	k.addTopic("orders", 1, 0, 1)
	b.registry = newFakeRegistry()
	b.registry.add("orders-value", 1, 10, "AVRO", orderV1)
	b.registry.add("orders-value", 2, 11, "AVRO", orderV2)
	b.registry.add("payments-value", 1, 20, "JSON", `{"type":"object"}`)
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	return m, b, b.registry
}

func TestTreeSchemaSection(t *testing.T) {
	m, _, _ := registryTree(t)
	root := m.tree.Roots()[0]
	if got := names(root.Children); len(got) != 5 || got[3] != "Schema Registry (2)" || got[4] != "ACLs" {
		t.Fatalf("sections = %v", got)
	}
	subs := section(t, m, "events", secSchemas).Children
	if len(subs) != 2 || subs[0].Name != "orders-value" || subs[0].Detail != "v2 AVRO" {
		t.Fatalf("subjects = %+v", subs[0])
	}

	m2, _, _ := newKafkaModel(t, config.Connection{})
	m2.setFocus(focusTree)
	pressAll(t, m2, "l")
	if got := names(m2.tree.Roots()[0].Children); !slices.Equal(got, []string{"Brokers (2)", "Topics (0)", "Consumer Groups (0)", "ACLs"}) {
		t.Fatalf("without a registry: %v", got)
	}
}

func TestTreeSchemaVersions(t *testing.T) {
	m, _, _ := registryTree(t)
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Section == secSchemas && n.Kind == tree.KindKafkaSection })
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindSchemaSubject && n.Key == "orders-value" })
	pressAll(t, m, "l")
	n := m.tree.Selected()
	if got := names(n.Children); !slices.Equal(got, []string{"v1", "v2"}) {
		t.Fatalf("versions = %v", got)
	}
}

// TestTreeRegistryPassword: a registry that wants a password asks for it
// without holding back the topics; Esc leaves the section's error.
func TestTreeRegistryPassword(t *testing.T) {
	m, b, k := newKafkaModel(t, withRegistry(config.Connection{}))
	k.addTopic("orders", 1, 0, 1)
	b.registry = newFakeRegistry()
	b.registry.errs["Subjects"] = &session.SecretRequiredError{Conn: "events", Kind: session.SecretSchemaRegistry}
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	if _, ok := m.overlay.(*overlay.Secret); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	if s := section(t, m, "events", secTopics); len(s.Children) != 1 {
		t.Fatalf("topics while the prompt is open = %+v", s)
	}
	pressAll(t, m, "esc")
	if s := section(t, m, "events", secTopics); len(s.Children) != 1 {
		t.Fatalf("topics after esc = %+v", s)
	}
	if s := section(t, m, "events", secSchemas); !strings.Contains(s.Err, "password") {
		t.Fatalf("schema section = %+v", s)
	}
}

func TestTreeSchemaError(t *testing.T) {
	m, b, k := newKafkaModel(t, withRegistry(config.Connection{}))
	k.addTopic("orders", 1, 0, 1)
	b.registry = newFakeRegistry()
	b.registry.errs["Subjects"] = errors.New("schema registry: 503 unavailable")
	m.setFocus(focusTree)
	pressAll(t, m, "l")
	if s := section(t, m, "events", secSchemas); !strings.Contains(s.Err, "503 unavailable") {
		t.Fatalf("schema section = %+v", s)
	}
	if s := section(t, m, "events", secTopics); s.Err != "" || len(s.Children) != 1 {
		t.Fatalf("topics = %+v", s)
	}
}

func TestTreeSchemaOpen(t *testing.T) {
	m, _, _ := registryTree(t)
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Section == secSchemas && n.Kind == tree.KindKafkaSection })
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindSchemaSubject && n.Key == "orders-value" })
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindSchemaVersion && n.Name == "v1" })
	pressAll(t, m, "enter")
	st, ok := m.activeTab().(*schemaTab)
	if !ok || st.subject != "orders-value" || !strings.Contains(st.header(), "v1 of 2") {
		t.Fatalf("tab = %T %+v", m.activeTab(), st)
	}
}

func TestTreeSchemaFilter(t *testing.T) {
	m, _, _ := registryTree(t)
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Section == secSchemas && n.Kind == tree.KindKafkaSection })
	pressAll(t, m, "F")
	typeText(t, m, "order")
	pressAll(t, m, "enter")
	if got := names(section(t, m, "events", secSchemas).Children); !slices.Equal(got, []string{"orders-value"}) {
		t.Fatalf("subjects = %v", got)
	}
}
