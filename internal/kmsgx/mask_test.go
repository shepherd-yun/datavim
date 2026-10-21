package kmsgx

import (
	"encoding/json"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

func TestMask(t *testing.T) {
	rule := func(fields []string, headers ...string) []config.MaskRule {
		return []config.MaskRule{{Env: "prod", Fields: fields, Headers: headers}}
	}
	for _, tc := range []struct {
		name          string
		rules         []config.MaskRule
		rec           kafka.Record
		decoded       []byte // the value's decoded JSON
		value, key    string
		headers       []string // their values after
		masked        bool
		valueNullKept bool
	}{
		{
			name: "path", rules: rule([]string{"value.customer.phone"}),
			rec:   kafka.Record{Value: []byte(`{"id":1, "customer":{"phone":"555","name":"a"}}`)},
			value: `{"id":1,"customer":{"phone":"******","name":"a"}}`, masked: true,
		},
		{
			name: "array elements", rules: rule([]string{"value.cards[].number"}),
			rec:   kafka.Record{Value: []byte(`{"cards":[{"number":"4111","exp":"01/30"},{"number":"5500"}]}`)},
			value: `{"cards":[{"number":"******","exp":"01/30"},{"number":"******"}]}`, masked: true,
		},
		{
			name: "any key", rules: rule([]string{"value.*.email"}),
			rec:   kafka.Record{Value: []byte(`{"a":{"email":"x@y"},"b":{"email":"z@w","n":1},"c":2}`)},
			value: `{"a":{"email":"******"},"b":{"email":"******","n":1},"c":2}`, masked: true,
		},
		{
			name: "top-level array", rules: rule([]string{"value.[].ssn"}),
			rec:   kafka.Record{Value: []byte(`[{"ssn":"1"},{"ssn":"2"}]`)},
			value: `[{"ssn":"******"},{"ssn":"******"}]`, masked: true,
		},
		{
			name: "missing path", rules: rule([]string{"value.customer.phone"}),
			rec:   kafka.Record{Value: []byte(`{"id": 1}`)},
			value: `{"id": 1}`,
		},
		{
			name: "numbers stay", rules: rule([]string{"value.secret"}),
			rec:   kafka.Record{Value: []byte(`{"n":12345678901234567890,"secret":7,"f":1.50}`)},
			value: `{"n":12345678901234567890,"secret":"******","f":1.50}`, masked: true,
		},
		{
			name: "not JSON", rules: rule([]string{"value.x"}),
			rec:   kafka.Record{Value: []byte("hello 555")},
			value: `"******"`, masked: true,
		},
		{
			name: "whole value", rules: rule([]string{"value"}),
			rec:   kafka.Record{Value: []byte(`{"a":1}`), Key: []byte("k1")},
			value: `"******"`, key: "k1", masked: true,
		},
		{
			name: "tombstone", rules: rule([]string{"value"}),
			rec:   kafka.Record{ValueNull: true},
			value: "", valueNullKept: true,
		},
		{
			name: "key path", rules: rule([]string{"key.ssn"}),
			rec: kafka.Record{Key: []byte(`{"ssn":"9","id":3}`), Value: []byte("v")},
			key: `{"ssn":"******","id":3}`, value: "v", masked: true,
		},
		{
			name: "headers", rules: rule(nil, "x-user-*", "authorization"),
			rec: kafka.Record{Value: []byte("v"), Headers: []kafka.Header{
				{Key: "X-User-Id", Value: []byte("42")}, {Key: "Authorization", Value: []byte("Bearer t")}, {Key: "trace", Value: []byte("abc")},
			}},
			value: "v", headers: []string{"******", "******", "abc"}, masked: true,
		},
		{
			name: "decoded wire value", rules: rule([]string{"value.phone"}),
			rec:     kafka.Record{Value: []byte{0, 0, 0, 0, 7, 2, 'x'}},
			decoded: []byte(`{"phone":"555","id":1}`),
			value:   `{"phone":"******","id":1}`, masked: true,
		},
	} {
		mk := NewMasker(tc.rules, "events", "prod", "orders")
		got, masked := mk.Mask(tc.rec, nil, tc.decoded)
		if masked != tc.masked {
			t.Errorf("%s: masked = %v", tc.name, masked)
		}
		if string(got.Value) != tc.value {
			t.Errorf("%s: value = %s, want %s", tc.name, got.Value, tc.value)
		}
		if tc.key != "" && string(got.Key) != tc.key {
			t.Errorf("%s: key = %s, want %s", tc.name, got.Key, tc.key)
		}
		if tc.valueNullKept && !got.ValueNull {
			t.Errorf("%s: a tombstone is no longer one", tc.name)
		}
		for i, h := range tc.headers {
			if string(got.Headers[i].Value) != h {
				t.Errorf("%s: header %d = %s, want %s", tc.name, i, got.Headers[i].Value, h)
			}
		}
		if masked && len(tc.rec.Headers) > 0 && string(tc.rec.Headers[0].Value) == "******" {
			t.Errorf("%s: Mask changed its input", tc.name)
		}
	}
}

func TestNewMasker(t *testing.T) {
	rules := []config.MaskRule{
		{Env: "prod", Topics: []string{"payments.*"}, Fields: []string{"value"}},
		{Connections: []string{"dev-k"}, Fields: []string{"key"}},
	}
	if NewMasker(rules, "k", "prod", "payments.eu") == nil {
		t.Fatal("env + topic glob did not match")
	}
	if NewMasker(rules, "k", "dev", "payments.eu") != nil {
		t.Fatal("env matched dev")
	}
	if NewMasker(rules, "k", "prod", "orders") != nil {
		t.Fatal("topic glob matched orders")
	}
	if NewMasker(rules, "dev-k", "", "anything") == nil {
		t.Fatal("connection rule did not match")
	}
	var none *Masker
	r := kafka.Record{Value: []byte("v")}
	if got, masked := none.Mask(r, nil, nil); masked || string(got.Value) != "v" {
		t.Fatal("a nil Masker masked")
	}
	if none.Pending(false) {
		t.Fatal("a nil Masker is pending")
	}
}

func TestPending(t *testing.T) {
	paths := NewMasker([]config.MaskRule{{Env: "prod", Fields: []string{"value.a"}}}, "k", "prod", "t")
	if !paths.Pending(false) || paths.Pending(true) {
		t.Fatal("value path: Pending wrong")
	}
	headers := NewMasker([]config.MaskRule{{Env: "prod", Headers: []string{"h"}}}, "k", "prod", "t")
	if headers.Pending(false) || headers.Pending(true) {
		t.Fatal("header rule: pending")
	}
}

func FuzzMask(f *testing.F) {
	f.Add([]byte(`{"a":{"b":[1,{"c":"d"}]}}`))
	f.Add([]byte("not json"))
	f.Add([]byte(`[{"c":1},[2]]`))
	mk := NewMasker([]config.MaskRule{{Env: "prod", Fields: []string{"value.a.b[].c", "value.[].c", "value.*.x"}}}, "k", "prod", "t")
	f.Fuzz(func(t *testing.T, v []byte) {
		got, masked := mk.Mask(kafka.Record{Value: v}, nil, nil)
		if !masked && string(got.Value) != string(v) {
			t.Fatalf("changed without masking: %q → %q", v, got.Value)
		}
		if masked && !json.Valid(got.Value) {
			t.Fatalf("masked into invalid JSON: %q → %q", v, got.Value)
		}
	})
}

func TestMaskJSON(t *testing.T) {
	mk := NewMasker([]config.MaskRule{{Env: "prod", Fields: []string{"value.phone", "key"}}}, "k", "prod", "t")
	if got, ch := mk.MaskJSON(false, []byte(`{"phone":"5","id":1}`)); !ch || string(got) != `{"phone":"******","id":1}` {
		t.Fatalf("value = %s %v", got, ch)
	}
	if got, ch := mk.MaskJSON(false, []byte(`{"id":1}`)); ch || string(got) != `{"id":1}` {
		t.Fatalf("no path = %s %v", got, ch)
	}
	if got, ch := mk.MaskJSON(true, []byte(`{"id":1}`)); !ch || string(got) != `"******"` {
		t.Fatalf("whole key = %s %v", got, ch)
	}
	var none *Masker
	if got, ch := none.MaskJSON(false, []byte(`1`)); ch || string(got) != "1" {
		t.Fatal("nil masker")
	}
}
