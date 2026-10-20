package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
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
