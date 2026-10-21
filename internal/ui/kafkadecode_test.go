package ui

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/kmsgx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
)

const orderAvroSchema = `{"type":"record","name":"Order","fields":[{"name":"id","type":"long"}]}`

// decodeModel is orders (one partition, 3 messages) on a connection with a
// registry where id 12 is orders-value v3, an Avro Order.
func decodeModel(t *testing.T, cfg config.Connection) (*Model, *fakeBackend, *fakeKafka) {
	t.Helper()
	m, b, k := newKafkaModel(t, cfg)
	b.registry = newFakeRegistry()
	b.registry.add("orders-value", 3, 12, "AVRO", orderAvroSchema)
	k.addTopic("orders", 1, 0, 3)
	return m, b, k
}

func avroOrder(t *testing.T, id int) []byte {
	t.Helper()
	b, err := kmsgx.Encode(kafka.Schema{ID: 12, Type: "AVRO", Text: orderAvroSchema}, nil, "", []byte(`{"id":`+strconv.Itoa(id)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func setValue(k *fakeKafka, p int32, o int64, v []byte) {
	r := fakeRecord("orders", p, o)
	r.Value, r.Size = v, len(r.Key)+len(v)
	k.setRecord(r)
}

// valuePreview is the VALUE cell of the row showing offset o.
func valuePreview(t *testing.T, tt *topicTab, o int64) (value, size string) {
	t.Helper()
	rs := tt.msgGrid.Result()
	for row, i := range tt.msgs.shown {
		if tt.msgs.recs[i].Offset == o {
			cells := rs.Rows[row]
			return cells[len(cells)-1].Text, cells[len(cells)-2].Text
		}
	}
	t.Fatalf("no row for offset %d", o)
	return "", ""
}

func TestDecodeAvroValue(t *testing.T) {
	m, _, k := decodeModel(t, withRegistry(config.Connection{}))
	for o := range int64(3) {
		setValue(k, 0, o, avroOrder(t, int(o)+1))
	}
	tt := openTopicTab(t, m, "orders")
	if v, _ := valuePreview(t, tt, 0); v != `{"id":1}` {
		t.Fatalf("VALUE = %q", v)
	}
	cursorOn(t, tt, 0, 1)
	pressAll(t, m, "K")
	if s := screen(m); !strings.Contains(s, "schema id 12 · orders-value · v3 · AVRO") || !strings.Contains(s, `"id": 2`) {
		t.Fatalf("detail:\n%s", s)
	}
	pressAll(t, m, "esc")
	text, _, ok := tt.yankText(yankValue(t, tt))
	if !ok || !strings.Contains(text, `"id": 2`) {
		t.Fatalf("yl = %q", text)
	}
}

func TestDecodeFailureIsolated(t *testing.T) {
	m, _, k := decodeModel(t, withRegistry(config.Connection{}))
	setValue(k, 0, 0, avroOrder(t, 1))
	setValue(k, 0, 1, []byte{0, 0, 0, 0, 7, 0xAA})
	setValue(k, 0, 2, []byte{0, 1, 2})
	tt := openTopicTab(t, m, "orders")
	if v, _ := valuePreview(t, tt, 0); v != `{"id":1}` {
		t.Fatalf("first VALUE = %q", v)
	}
	if v, size := valuePreview(t, tt, 1); !strings.HasPrefix(v, "0000000007aa") || !strings.HasPrefix(size, "!") {
		t.Fatalf("second VALUE = %q size %q", v, size)
	}
	if _, size := valuePreview(t, tt, 2); strings.HasPrefix(size, "!") {
		t.Fatalf("a 3-byte value is not in the wire format: size %q", size)
	}
	cursorOn(t, tt, 0, 1)
	pressAll(t, m, "K")
	if s := screen(m); !strings.Contains(s, "schema 7: not found") {
		t.Fatalf("detail:\n%s", s)
	}
	pressAll(t, m, "esc")
	cursorOn(t, tt, 0, 2)
	pressAll(t, m, "K")
	if s := screen(m); strings.Contains(s, "schema ") {
		t.Fatalf("detail of a non-wire value:\n%s", s)
	}
}

// TestDecodeRetriesTransientFailure: a failed decode isn't kept past a
// reload of the view, so a registry that was down decodes on <Space>r.
func TestDecodeRetriesTransientFailure(t *testing.T) {
	m, b, k := decodeModel(t, withRegistry(config.Connection{}))
	setValue(k, 0, 0, avroOrder(t, 1))
	b.registry.mu.Lock()
	b.registry.errs["SchemaByID"] = errors.New("schema registry: 503 unavailable")
	b.registry.mu.Unlock()
	tt := openTopicTab(t, m, "orders")
	if _, size := valuePreview(t, tt, 0); !strings.HasPrefix(size, "!") {
		t.Fatalf("size %q, want a decode failure", size)
	}
	b.registry.mu.Lock()
	delete(b.registry.errs, "SchemaByID")
	b.registry.mu.Unlock()
	pressAll(t, m, "space", "r")
	drainAll(t, m, nil)
	if v, _ := valuePreview(t, tt, 0); v != `{"id":1}` {
		t.Fatalf("VALUE after a reload = %q", v)
	}
}

func TestDecodedFilter(t *testing.T) {
	m, _, k := decodeModel(t, withRegistry(config.Connection{}))
	for o := range int64(3) {
		setValue(k, 0, o, avroOrder(t, int(o)+1))
	}
	tt := openTopicTab(t, m, "orders")
	pressAll(t, m, "F")
	typeText(t, m, "value.id = 2")
	pressAll(t, m, "enter")
	if len(tt.msgs.shown) != 1 || tt.msgs.recs[tt.msgs.shown[0]].Offset != 1 {
		t.Fatalf("shown = %v", tt.msgs.shown)
	}
}

func TestNoRegistryNoDecode(t *testing.T) {
	m, b, k := decodeModel(t, config.Connection{})
	setValue(k, 0, 0, avroOrder(t, 1))
	tt := openTopicTab(t, m, "orders")
	if v, size := valuePreview(t, tt, 0); strings.Contains(v, "id") || strings.HasPrefix(size, "!") {
		t.Fatalf("VALUE = %q size %q", v, size)
	}
	if b.registryOpens != 0 || len(b.registry.calls) != 0 {
		t.Fatalf("registry used: %d opens, calls %v", b.registryOpens, b.registry.calls)
	}
}

func TestDecodeStaleGen(t *testing.T) {
	m, _, k := decodeModel(t, withRegistry(config.Connection{}))
	setValue(k, 0, 0, avroOrder(t, 1))
	tt := openTopicTab(t, m, "orders")
	stale := msgsDecodedMsg{tabID: tt.id, gen: tt.msgs.gen - 1, dec: map[recID]decodedRec{
		{0, 1}: {Value: &decodedPart{JSON: []byte(`{"stale":true}`)}},
	}}
	tt.Update(stale)
	if _, ok := tt.msgs.decoded[recID{0, 1}]; ok {
		t.Fatal("a decode of an older load was kept")
	}
}

func yankValue(t *testing.T, tt *topicTab) keymap.Action {
	t.Helper()
	if !tt.msgs.grid.SelectColumn("VALUE") {
		t.Fatal("no VALUE column")
	}
	return keymap.YankCell
}
