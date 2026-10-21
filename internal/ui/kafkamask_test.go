package ui

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

const (
	phone       = "5550100"
	phoneRecord = `{"id":1,"customer":{"phone":"5550100","name":"Ann"}}`
)

var phoneRules = []config.MaskRule{{Env: "prod", Topics: []string{"orders"}, Fields: []string{"value.customer.phone"}, Headers: []string{"authorization"}}}

// maskModel is orders (one partition, 3 messages, the phone in p0:1) on a
// prod connection whose rules mask the phone and the authorization header.
func maskModel(t *testing.T, cfg config.Connection) (*Model, *fakeBackend, *fakeKafka) {
	t.Helper()
	cfg.Env = config.EnvProd
	m, b, k := newKafkaModel(t, cfg)
	m.SetMasks(phoneRules)
	k.addTopic("orders", 1, 0, 3)
	r := fakeRecord("orders", 0, 1)
	r.Value = []byte(phoneRecord)
	r.Headers = []kafka.Header{{Key: "Authorization", Value: []byte("Bearer tok3n")}, {Key: "trace", Value: []byte("t1")}}
	k.setRecord(r)
	return m, b, k
}

// leaks reports where s shows the phone or the token, in any encoding.
func leaks(s string) bool {
	for _, secret := range []string{phone, "tok3n"} {
		for _, form := range []string{secret, hex.EncodeToString([]byte(secret)), base64.StdEncoding.EncodeToString([]byte(secret))} {
			if strings.Contains(s, form) {
				return true
			}
		}
	}
	return strings.Contains(s, base64.StdEncoding.EncodeToString([]byte(phoneRecord))) ||
		strings.Contains(s, hex.EncodeToString([]byte(phoneRecord)))
}

func TestMaskEveryExit(t *testing.T) {
	m, _, _ := maskModel(t, config.Connection{})
	tt := openMessages(t, m, "orders")
	if !strings.Contains(tt.msgsHeader(), "masked") {
		t.Fatalf("header = %q", tt.msgsHeader())
	}
	exits := map[string]string{}
	v, _ := valuePreview(t, tt, 1)
	exits["VALUE cell"] = v
	exits["screen"] = screen(m)
	cursorOn(t, tt, 0, 1)
	r, _ := tt.msgs.current()
	for _, f := range []valueFormat{fmtAuto, fmtText, fmtJSON, fmtHex, fmtBase64} {
		lines, _ := renderBytes(r.Value, r.ValueNull, f)
		exits["format "+f.String()] = strings.Join(lines, "\n")
	}
	pressAll(t, m, "K")
	for range 5 {
		exits["detail "+tt.msgs.format.String()] += screen(m)
		pressAll(t, m, "v")
	}
	pressAll(t, m, "esc")
	text, _, _ := tt.yankText(yankValue(t, tt))
	exits["yl VALUE"] = text
	tt.msgs.grid.SelectColumn("HEADERS")
	text, _, _ = tt.yankText(keymap.YankCell)
	exits["yl HEADERS"] = text
	text, _, _ = tt.yankText(keymap.YankRow)
	exits["yy"] = text
	src := tt.msgsExport()
	var csv, jsonl strings.Builder
	for _, row := range src.rs.Rows {
		for _, c := range row {
			csv.WriteString(c.Text + "\t")
		}
	}
	for _, row := range src.jsonl.Rows {
		jsonl.WriteString(row[0].Text + "\n")
	}
	exits["export table"], exits["export JSONL"] = csv.String(), jsonl.String()
	for name, s := range exits {
		if leaks(s) {
			t.Errorf("%s shows the secret:\n%s", name, s)
		}
	}
	if !strings.Contains(exits["yl VALUE"], kmsgx.Masked) || !strings.Contains(exits["yl HEADERS"], kmsgx.Masked) {
		t.Errorf("not masked: %q / %q", exits["yl VALUE"], exits["yl HEADERS"])
	}
	if !strings.Contains(exits["yl VALUE"], "Ann") {
		t.Errorf("unmasked fields are gone: %q", exits["yl VALUE"])
	}
	pressAll(t, m, "F")
	typeText(t, m, "value ~ "+phone)
	pressAll(t, m, "enter")
	if len(tt.msgs.shown) != 0 {
		t.Errorf("a filter on the masked phone finds %d messages", len(tt.msgs.shown))
	}
}

func TestMaskTail(t *testing.T) {
	m, _, k := maskModel(t, config.Connection{})
	tt := openMessages(t, m, "orders")
	_, cmd := m.Update(uitest.Key("T"))
	polls := step(t, m, cmd)
	r := fakeRecord("orders", 0, 3)
	r.Value = []byte(phoneRecord)
	k.mu.Lock()
	k.tailQ = append(k.tailQ, r)
	k.mu.Unlock()
	stepAll(t, m, polls)
	got := tt.msgs.recs[len(tt.msgs.recs)-1]
	if got.Offset != 3 || leaks(string(got.Value)) {
		t.Fatalf("tailed message = %d %s", got.Offset, got.Value)
	}
}

func TestMaskWireValue(t *testing.T) {
	m, b, k := maskModel(t, withRegistry(config.Connection{}))
	const schema = `{"type":"record","name":"Order","fields":[{"name":"id","type":"long"},{"name":"customer","type":{"type":"record","name":"C","fields":[{"name":"phone","type":"string"},{"name":"name","type":"string"}]}}]}`
	b.registry = newFakeRegistry()
	b.registry.add("orders-value", 1, 12, "AVRO", schema)
	wire, err := kmsgx.Encode(kafka.Schema{ID: 12, Type: "AVRO", Text: schema}, nil, "", []byte(phoneRecord))
	if err != nil {
		t.Fatal(err)
	}
	setValue(k, 0, 1, wire)
	setValue(k, 0, 2, []byte{0, 0, 0, 0, 99, 1, 2, 3}) // schema 99: not found
	drainAll(t, m, m.openTopic("events", "orders"))
	tt := m.activeTab().(*topicTab)
	// before the decode comes back
	tt2 := newTopicTab(99, b, m.keys, m.settings, "events", "orders")
	tt2.msgs.masker = tt.msgs.masker
	recs := tt2.maskIn([]kafka.Record{{Partition: 0, Offset: 1, Value: wire}}, nil)
	if string(recs[0].Value) != `"******"` {
		t.Fatalf("pending wire value = %q", recs[0].Value)
	}
	v, _ := valuePreview(t, tt, 1)
	if leaks(v) || !strings.Contains(v, "Ann") || !strings.Contains(v, kmsgx.Masked) {
		t.Fatalf("decoded VALUE = %q", v)
	}
	if v, _ := valuePreview(t, tt, 2); leaks(v) || !strings.Contains(v, kmsgx.Masked) || strings.Contains(v, "0000000063") {
		t.Fatalf("undecodable VALUE = %q", v)
	}
	for _, f := range []valueFormat{fmtHex, fmtBase64} {
		for _, o := range []int64{1, 2} {
			for _, rec := range tt.msgs.recs {
				if rec.Offset == o {
					lines, _ := renderBytes(rec.Value, rec.ValueNull, f)
					if leaks(strings.Join(lines, "")) {
						t.Errorf("offset %d in %s leaks", o, f)
					}
				}
			}
		}
	}
}

func TestMaskResend(t *testing.T) {
	m, _, _ := maskModel(t, config.Connection{ReadOnly: new(bool)})
	tt := openMessages(t, m, "orders")
	cursorOn(t, tt, 0, 1)
	pressAll(t, m, "p")
	if _, ok := m.activeTab().(*producerTab); ok || !strings.Contains(tt.status, "has masked fields") {
		t.Fatalf("active %T status %q", m.activeTab(), tt.status)
	}
	cmd := tt.resend(true)
	msg, ok := cmd().(resendPickMsg)
	if !ok || msg.count != 2 || msg.masked != 1 {
		t.Fatalf("P = %#v", msg)
	}
}

func TestMaskNoRule(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{Env: config.EnvDev})
	m.SetMasks(phoneRules)
	k.addTopic("orders", 1, 0, 2)
	r := fakeRecord("orders", 0, 1)
	r.Value = []byte(phoneRecord)
	k.setRecord(r)
	tt := openMessages(t, m, "orders")
	if strings.Contains(tt.msgsHeader(), "masked") {
		t.Fatalf("header = %q", tt.msgsHeader())
	}
	if v, _ := valuePreview(t, tt, 1); !strings.Contains(v, phone) {
		t.Fatalf("VALUE = %q", v)
	}
}
