package ui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/kmsgx"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

const twoDrafts = `key: ord-1
partition: 0

{"id": 1}
===
key: ord-2
{"id": 2}`

// openProducer opens the producer console of orders with text in it,
// in NORMAL mode, the cursor on line (0-based).
func openProducer(t *testing.T, m *Model, text string, line int) *producerTab {
	t.Helper()
	drainAll(t, m, m.openProducer("events", "orders"))
	p, ok := m.activeTab().(*producerTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	pressAll(t, m, "esc")
	p.editor.SetValue(text)
	p.editor.SetCursor(line, 0)
	return p
}

func TestProducerSendCurrent(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 10)
	p := openProducer(t, m, twoDrafts, 6)
	if p.Title() != "produce:orders" {
		t.Fatalf("title %q", p.Title())
	}
	pressAll(t, m, "enter")
	if len(k.produced) != 1 || len(k.produced[0].recs) != 1 || string(k.produced[0].recs[0].Key) != "ord-2" || k.produced[0].recs[0].Partition != -1 {
		t.Fatalf("produced = %+v", k.produced)
	}
	if s := screen(m); !strings.Contains(s, "#1 line 6 → p0 · offset 10") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestProducerSendAll(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 10)
	openProducer(t, m, twoDrafts, 0)
	pressAll(t, m, "R")
	if len(k.produced) != 1 || len(k.produced[0].recs) != 2 {
		t.Fatalf("produced = %+v", k.produced)
	}
	if s := screen(m); !strings.Contains(s, "#2 line 6") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestProducerParseErrorJumps(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 10)
	p := openProducer(t, m, "key: a\n\n{}\n===\nkey: b\npartition: x\n\n{}", 0)
	pressAll(t, m, "R")
	if len(k.produced) != 0 {
		t.Fatal("nothing is sent when a message does not parse")
	}
	if line, _ := p.editor.Cursor(); line != 5 || !strings.Contains(p.status, "line 6: partition must be a number ≥ 0") {
		t.Fatalf("cursor line %d, status %q", line, p.status)
	}
}

func TestProducerInvalidJSONConfirm(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 10)
	openProducer(t, m, "key: a\n\n{not json", 0)
	pressAll(t, m, "enter")
	if _, ok := m.overlay.(*overlay.Confirm); !ok || !strings.Contains(screen(m), "message at line 1 is not valid JSON · send as text?") {
		t.Fatalf("overlay %T:\n%s", m.overlay, screen(m))
	}
	pressAll(t, m, "y")
	if len(k.produced) != 1 || string(k.produced[0].recs[0].Value) != "{not json" {
		t.Fatalf("produced = %+v", k.produced)
	}
}

func TestProducerProdConfirm(t *testing.T) {
	off := false
	m, _, k := newKafkaModel(t, config.Connection{Env: config.EnvProd, ReadOnly: &off})
	k.addTopic("orders", 3, 0, 10)
	openProducer(t, m, twoDrafts, 0)
	pressAll(t, m, "R")
	if _, ok := m.overlay.(*overlay.Confirm); !ok || !strings.Contains(screen(m), "Send 2 messages to orders on events?") {
		t.Fatalf("overlay %T:\n%s", m.overlay, screen(m))
	}
	if len(k.produced) != 0 {
		t.Fatal("nothing sent before y")
	}
	pressAll(t, m, "y")
	if len(k.produced) != 1 {
		t.Fatal("sent after y")
	}
}

func TestProducerReadOnly(t *testing.T) {
	on := true
	m, _, k := newKafkaModel(t, config.Connection{ReadOnly: &on})
	k.addTopic("orders", 3, 0, 10)
	p := openProducer(t, m, twoDrafts, 0)
	pressAll(t, m, "R")
	if len(k.produced) != 0 || p.status != readOnlyHint {
		t.Fatalf("produced %d, status %q", len(k.produced), p.status)
	}
}

func TestProducerDraftSaved(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 10)
	openProducer(t, m, "key: kept\n\n{}", 0)
	pressAll(t, m, ":")
	typeText(t, m, "q")
	pressAll(t, m, "enter")
	if got := m.store.(*fakeStore).draft(producerDraftKey("events", "orders")); got != "key: kept\n\n{}" {
		t.Fatalf("draft = %q", got)
	}
	drainAll(t, m, m.openProducer("events", "orders"))
	if p := m.activeTab().(*producerTab); p.editor.Value() != "key: kept\n\n{}" {
		t.Fatalf("reopened = %q", p.editor.Value())
	}
}

func TestProducerNewDraftTemplate(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 10)
	drainAll(t, m, m.openProducer("events", "orders"))
	if p := m.activeTab().(*producerTab); p.editor.Value() != "# topic: orders\nkey: \n\n{}" {
		t.Fatalf("new draft = %q", p.editor.Value())
	}
}

func TestProducerNotOnConnection(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 10)
	m.setFocus(focusTree)
	pressAll(t, m, "space", "c")
	if !strings.Contains(m.status, "select a topic first") {
		t.Fatalf("status %q", m.status)
	}
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Section == "topics" })
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindTopic })
	pressAll(t, m, "space", "c")
	if p, ok := m.activeTab().(*producerTab); !ok || p.topic != "orders" {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	drainAll(t, m, m.openTopic("events", "orders"))
	pressAll(t, m, "space", "c")
	if _, ok := m.activeTab().(*producerTab); !ok {
		t.Fatalf("from a topic tab: %T", m.activeTab())
	}
}

func TestProducerMissingTopicError(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 10)
	k.produceErr = errors.New("topic orders doesn't exist")
	openProducer(t, m, twoDrafts, 0)
	pressAll(t, m, "enter")
	if s := screen(m); !strings.Contains(s, "topic orders doesn't exist") {
		t.Fatalf("screen:\n%s", s)
	}
}

const orderNoteSchema = `{"type":"record","name":"Order","fields":[{"name":"id","type":"long"},{"name":"note","type":["null","string"],"default":null}]}`

// schemaProducer is the orders producer on a connection whose registry
// has orders-value v1 (id 12).
func schemaProducer(t *testing.T, cfg config.Connection, text string) (*Model, *fakeKafka, *producerTab) {
	t.Helper()
	m, b, k := newKafkaModel(t, cfg)
	k.addTopic("orders", 1, 0, 0)
	b.registry = newFakeRegistry()
	b.registry.add("orders-value", 1, 12, "AVRO", orderNoteSchema)
	return m, k, openProducer(t, m, text, 0)
}

func TestProduceWithSchema(t *testing.T) {
	m, k, _ := schemaProducer(t, withRegistry(config.Connection{}), "value-schema: orders-value\n{\"id\":1,\"note\":null}")
	pressAll(t, m, "R")
	drainAll(t, m, nil)
	if len(k.produced) != 1 {
		t.Fatalf("produced = %+v", k.produced)
	}
	v := k.produced[0].recs[0].Value
	if !bytes.HasPrefix(v, []byte{0, 0, 0, 0, 12}) {
		t.Fatalf("value = % x", v)
	}
	js, err := kmsgx.Decode(kafka.Schema{ID: 12, Type: "AVRO", Text: orderNoteSchema}, nil, v[5:])
	if err != nil || string(js) != `{"id":1,"note":null}` {
		t.Fatalf("decoded = %s, %v", js, err)
	}
}

// TestSerializeStopsWithTheTab: closing the console cancels a pending
// serialization's registry calls.
func TestSerializeStopsWithTheTab(t *testing.T) {
	_, _, p := schemaProducer(t, withRegistry(config.Connection{}), "")
	recs, err := kmsgx.ParseDraft("value-schema: orders-value\n{\"id\":1,\"note\":null}")
	if err != nil {
		t.Fatal(err)
	}
	cmd := p.serialize(recs)
	p.shutdown()()
	msg, ok := cmd().(serializedMsg)
	if !ok || !errors.Is(msg.err, context.Canceled) {
		t.Fatalf("msg = %+v", msg)
	}
}

func TestProduceSchemaError(t *testing.T) {
	m, k, p := schemaProducer(t, withRegistry(config.Connection{}), "value-schema: orders-value\n{\"id\":1}\n===\nvalue-schema: orders-value\n{\"note\":null}")
	pressAll(t, m, "R")
	drainAll(t, m, nil)
	if len(k.produced) != 0 {
		t.Fatalf("produced = %+v", k.produced)
	}
	if !strings.HasPrefix(p.status, "line 4: value-schema orders-value: ") {
		t.Fatalf("status = %q", p.status)
	}
	if s := screen(m); !strings.Contains(s, "line 4: value-schema orders-value") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestProduceEncodedBody(t *testing.T) {
	m, k, _ := schemaProducer(t, config.Connection{}, "value-encoding: base64\nAP8=")
	pressAll(t, m, "R")
	drainAll(t, m, nil)
	if len(k.produced) != 1 || !bytes.Equal(k.produced[0].recs[0].Value, []byte{0, 0xff}) {
		t.Fatalf("produced = %+v", k.produced)
	}
}

func TestProduceSchemaNoRegistry(t *testing.T) {
	m, k, p := schemaProducer(t, config.Connection{}, "key: k\nvalue-schema: orders-value\n{\"id\":1}")
	pressAll(t, m, "R")
	drainAll(t, m, nil)
	if len(k.produced) != 0 || p.status != "line 1: no schema_registry_url for this connection" {
		t.Fatalf("produced %+v status %q", k.produced, p.status)
	}
}

func TestResendOne(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 3)
	k.addTopic("orders-dlq-replay", 1, 0, 0)
	r := fakeRecord("orders", 0, 1)
	r.Headers = []kafka.Header{{Key: "trace", Value: []byte("abc")}}
	k.setRecord(r)
	openProducer(t, m, "key: a\nv", 0)
	tt := openTopicTab(t, m, "orders")
	cursorOn(t, tt, 0, 1)
	pressAll(t, m, "p")
	if _, ok := m.overlay.(*overlay.Picker); !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, tt.status)
	}
	pressAll(t, m, "enter") // the current topic is first
	p, ok := m.activeTab().(*producerTab)
	if !ok || p.Title() != "produce:orders" {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	want := "key: a\nv\n===\n" + kmsgx.FormatDraft([]kafka.Record{r})
	if got := p.editor.Value(); got != strings.TrimRight(want, "\n") && got != want {
		t.Fatalf("draft:\n%q\nwant\n%q", got, want)
	}
	if len(k.produced) != 0 {
		t.Fatal("copying sent the message")
	}
	if !strings.Contains(m.status+p.status, "copied 1 message to produce:orders · nothing is sent until you run them") {
		t.Fatalf("status %q / %q", m.status, p.status)
	}
}

func TestResendShownOtherTopic(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 12)
	k.addTopic("orders-dlq-replay", 1, 0, 0)
	tt := openTopicTab(t, m, "orders")
	pressAll(t, m, "F")
	typeText(t, m, "key ~ k-1")
	pressAll(t, m, "enter")
	if len(tt.msgs.shown) != 3 { // k-1, k-10, k-11
		t.Fatalf("shown = %d", len(tt.msgs.shown))
	}
	pressAll(t, m, "P")
	typeText(t, m, "dlq")
	pressAll(t, m, "enter")
	p, ok := m.activeTab().(*producerTab)
	if !ok || p.Title() != "produce:orders-dlq-replay" {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	recs, err := kmsgx.ParseDraft(p.editor.Value())
	if err != nil || len(recs) != 3 {
		t.Fatalf("draft has %d messages (%v):\n%s", len(recs), err, p.editor.Value())
	}
}

// TestResendTooBig: a copy whose draft is over resendMaxBytes is refused
// and points to exporting the messages and sending the file.
func TestResendTooBig(t *testing.T) {
	old := resendMaxBytes
	resendMaxBytes = 200
	t.Cleanup(func() { resendMaxBytes = old })
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 12)
	tt := openTopicTab(t, m, "orders")
	pressAll(t, m, "P")
	if _, ok := m.activeTab().(*producerTab); ok || m.overlay != nil {
		t.Fatalf("copied anyway: tab %T, overlay %T", m.activeTab(), m.overlay)
	}
	if !strings.Contains(tt.status, "12 messages") || !strings.Contains(tt.status, "<space>xl") || !strings.Contains(tt.status, ":send") {
		t.Fatalf("status = %q", tt.status)
	}
}

func TestResendTruncated(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 3)
	r := fakeRecord("orders", 0, 1)
	r.Truncated = true
	k.setRecord(r)
	tt := openTopicTab(t, m, "orders")
	cursorOn(t, tt, 0, 1)
	pressAll(t, m, "p")
	if m.overlay != nil || tt.status != "p0:1 has only its first 1 MB here; it can't be sent again as it was" {
		t.Fatalf("overlay %T status %q", m.overlay, tt.status)
	}
	if len(m.tabs) != 1 {
		t.Fatal("a console opened")
	}
}

// writeJSONL writes n messages as JSON Lines export writes them.
func writeJSONL(t *testing.T, n int, bad int) string {
	t.Helper()
	var b strings.Builder
	for i := range n {
		if i+1 == bad {
			b.WriteString("{oops\n")
			continue
		}
		r := fakeRecord("orders", 0, int64(i))
		if i == 1 {
			r.Value = []byte{0, 0xff}
		}
		b.Write(kmsgx.JSON(r))
		b.WriteString("\n")
	}
	path := filepath.Join(t.TempDir(), "orders.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCommandLine(t *testing.T, m *Model, cmd string) {
	t.Helper()
	pressAll(t, m, ":")
	typeText(t, m, cmd)
	pressAll(t, m, "enter")
}

func TestSendFile(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 0)
	openProducer(t, m, "", 0)
	path := writeJSONL(t, 3, 0)
	runCommandLine(t, m, "send "+path)
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T, status %q", m.overlay, m.status)
	}
	if v := flatView(c); !strings.Contains(v, "Send 3 messages from orders.jsonl to orders?") {
		t.Fatalf("confirm:\n%s", v)
	}
	pressAll(t, m, "y")
	drainAll(t, m, nil)
	if len(k.produced) != 1 || len(k.produced[0].recs) != 3 {
		t.Fatalf("produced = %+v", k.produced)
	}
	if got := k.produced[0].recs[1].Value; !bytes.Equal(got, []byte{0, 0xff}) {
		t.Fatalf("binary value = % x", got)
	}
}

func TestSendFilePreview(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 0)
	openProducer(t, m, "", 0)
	runCommandLine(t, m, "send "+writeJSONL(t, 1001, 0))
	c, ok := m.overlay.(*overlay.Confirm)
	if !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	v := flatView(c)
	if !strings.Contains(v, "1,001 messages") || strings.Count(v, "k-") != 5 {
		t.Fatalf("confirm:\n%s", v)
	}
}

func TestSendFileBadLine(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 0)
	p := openProducer(t, m, "", 0)
	runCommandLine(t, m, "send "+writeJSONL(t, 3, 2))
	if len(k.produced) != 0 || m.overlay != nil {
		t.Fatalf("produced %+v overlay %T", k.produced, m.overlay)
	}
	if !strings.HasPrefix(p.status, "orders.jsonl line 2: ") || !strings.Contains(screen(m), "orders.jsonl line 2") {
		t.Fatalf("status %q", p.status)
	}
}

func TestSendOutsideProducer(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 0)
	openTopicTab(t, m, "orders")
	runCommandLine(t, m, "send x")
	if m.status != ":send works in a producer console" {
		t.Fatalf("status = %q", m.status)
	}
}

func TestSendFileReadOnly(t *testing.T) {
	on := true
	m, _, k := newKafkaModel(t, config.Connection{ReadOnly: &on})
	k.addTopic("orders", 1, 0, 0)
	p := openProducer(t, m, "", 0)
	runCommandLine(t, m, "send /nonexistent/x.jsonl")
	if p.status != readOnlyHint || m.overlay != nil {
		t.Fatalf("status %q overlay %T", p.status, m.overlay)
	}
}
