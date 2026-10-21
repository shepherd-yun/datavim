//go:build integration

// An external test package: kmsgx imports kafka, so package kafka's own
// tests can't use its codecs.
package kafka_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcredpanda "github.com/testcontainers/testcontainers-go/modules/redpanda"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
)

const (
	redpandaImage = "redpandadata/redpanda:v26.2.1" // kafka's integration matrix
	srUser        = "app"
	srPassword    = "right-pw-123"
)

func rctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

// startRegistry starts Redpanda with SASL and its Schema Registry behind
// HTTP basic auth (the same SASL users), and returns the broker and
// registry addresses.
func startRegistry(t *testing.T) (broker, registryURL string) {
	t.Helper()
	ctx := context.Background()
	ctr, err := tcredpanda.Run(ctx, redpandaImage,
		tcredpanda.WithEnableSASL(),
		tcredpanda.WithEnableKafkaAuthorization(),
		tcredpanda.WithEnableSchemaRegistryHTTPBasicAuth(),
		tcredpanda.WithNewServiceAccount(srUser, srPassword),
		tcredpanda.WithSuperusers(srUser),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	if broker, err = ctr.KafkaSeedBroker(ctx); err != nil {
		t.Fatal(err)
	}
	if registryURL, err = ctr.SchemaRegistryAddress(ctx); err != nil {
		t.Fatal(err)
	}
	return broker, registryURL
}

func sameJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("decoded is not JSON: %s", got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

const (
	orderAvro = `{"type":"record","name":"Order","namespace":"shop","fields":[` +
		`{"name":"id","type":"long"},{"name":"note","type":["null","string"],"default":null}]}`
	addressProto = `syntax = "proto3";
package shop;
message Address { string city = 1; }
`
	orderProto = `syntax = "proto3";
package shop;
import "shop/address.proto";
message Order { int64 id = 1; Address ship = 2; }
`
	eventJSON = `{"type":"object","properties":{"n":{"type":"integer"}}}`
)

// TestIntegrationSchemaRegistry registers an Avro, a Protobuf (with an
// import) and a JSON Schema subject, sends a message under each and decodes
// it again from what the Reader reads; then compatibility, a soft delete
// and a wrong password.
func TestIntegrationSchemaRegistry(t *testing.T) {
	broker, url := startRegistry(t)
	var dials atomic.Int32
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	reg, err := kafka.OpenRegistry(kafka.RegistryConfig{URL: url, User: srUser, Password: srPassword, Dial: dial})
	if err != nil {
		t.Fatal(err)
	}
	ctx := rctx(t)
	cfg := kafka.Config{Brokers: []string{broker}, SASL: "scram-sha-256", User: srUser, Password: srPassword}
	cl, err := kafka.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	addr, err := reg.Register(ctx, "shop-address", kafka.Schema{Type: "PROTOBUF", Text: addressProto})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		subject, topic, message, body, want string
		schema                              kafka.Schema
	}{
		{
			"avro-orders-value", "avro-orders", "", `{"id":88124,"note":{"string":"x"}}`, `{"id":88124,"note":"x"}`,
			kafka.Schema{Type: "AVRO", Text: orderAvro},
		},
		{
			"proto-orders-value", "proto-orders", "shop.Order", `{"id":"7","ship":{"city":"Oslo"}}`, `{"id":"7","ship":{"city":"Oslo"}}`,
			kafka.Schema{Type: "PROTOBUF", Text: orderProto, References: []kafka.SchemaRef{
				{Name: "shop/address.proto", Subject: "shop-address", Version: addr.Version},
			}},
		},
		{
			"json-events-value", "json-events", "", `{"n": 3}`, `{"n":3}`,
			kafka.Schema{Type: "JSON", Text: eventJSON},
		},
	}
	for _, tc := range cases {
		t.Run(tc.subject, func(t *testing.T) {
			s, err := reg.Register(ctx, tc.subject, tc.schema)
			if err != nil || s.ID == 0 || s.Version != 1 {
				t.Fatalf("register = %+v, %v", s, err)
			}
			refs, err := reg.Resolve(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := kmsgx.Encode(s, refs, tc.message, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if err := cl.CreateTopic(ctx, kafka.NewTopic{Name: tc.topic, Partitions: 1, Replication: -1}); err != nil {
				t.Fatal(err)
			}
			if _, err := cl.Produce(ctx, tc.topic, []kafka.ProduceRecord{{Value: wire}}); err != nil {
				t.Fatal(err)
			}
			r, err := kafka.OpenReader(ctx, cfg, kafka.ReaderConfig{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
			recs, err := r.Range(ctx, tc.topic, map[int32]kafka.Range{0: {From: 0, To: 1}})
			if err != nil || len(recs) != 1 {
				t.Fatalf("range = %+v, %v", recs, err)
			}
			id, payload, ok := kmsgx.Wire(recs[0].Value)
			if !ok || id != s.ID {
				t.Fatalf("wire: id %d ok %v", id, ok)
			}
			got, err := reg.SchemaByID(ctx, id)
			if err != nil || got.Type != tc.schema.Type || got.Subject != tc.subject {
				t.Fatalf("schema by id = %+v, %v", got, err)
			}
			refs, err = reg.Resolve(ctx, got)
			if err != nil {
				t.Fatal(err)
			}
			body, err := kmsgx.Decode(got, refs, payload)
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, body, tc.want)
		})
	}

	subs, err := reg.Subjects(ctx)
	if err != nil || len(subs) != 4 || subs[0].Name != "avro-orders-value" || subs[0].Type != "AVRO" {
		t.Fatalf("subjects = %+v, %v", subs, err)
	}

	// under FORWARD, dropping a field without a default breaks old readers
	if err := reg.SetCompatibility(ctx, "avro-orders-value", "FORWARD"); err != nil {
		t.Fatal(err)
	}
	if level, global, err := reg.Compatibility(ctx, "avro-orders-value"); err != nil || level != "FORWARD" || global {
		t.Fatalf("compatibility = %s %v, %v", level, global, err)
	}
	noID := kafka.Schema{Type: "AVRO", Text: `{"type":"record","name":"Order","namespace":"shop","fields":[` +
		`{"name":"note","type":["null","string"],"default":null}]}`}
	if ok, msgs, err := reg.Check(ctx, "avro-orders-value", noID); err != nil || ok {
		t.Fatalf("check of a dropped field = %v %q, %v", ok, msgs, err)
	}
	extra := kafka.Schema{Type: "AVRO", Text: `{"type":"record","name":"Order","namespace":"shop","fields":[` +
		`{"name":"id","type":"long"},{"name":"note","type":["null","string"],"default":null},` +
		`{"name":"tag","type":"string","default":""}]}`}
	if ok, msgs, err := reg.Check(ctx, "avro-orders-value", extra); err != nil || !ok {
		t.Fatalf("check of an added field = %v %q, %v", ok, msgs, err)
	}
	v2, err := reg.Register(ctx, "avro-orders-value", extra)
	if err != nil || v2.Version != 2 {
		t.Fatalf("register v2 = %+v, %v", v2, err)
	}
	if err := reg.DeleteVersion(ctx, "avro-orders-value", 2); err != nil {
		t.Fatal(err)
	}
	if vs, err := reg.Versions(ctx, "avro-orders-value"); err != nil || len(vs) != 1 || vs[0] != 1 {
		t.Fatalf("versions after a soft delete = %v, %v", vs, err)
	}
	if dials.Load() == 0 {
		t.Fatal("the registry didn't use the injected Dial")
	}

	bad, err := kafka.OpenRegistry(kafka.RegistryConfig{URL: strings.Replace(url, "://", "://"+srUser+":wrong-pw-456@", 1)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bad.Subjects(ctx)
	if !errors.Is(err, kafka.ErrRegistryAuth) || strings.Contains(err.Error(), "wrong-pw-456") {
		t.Fatalf("wrong password: %v", err)
	}
}
