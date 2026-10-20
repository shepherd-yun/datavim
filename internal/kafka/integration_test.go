//go:build integration

package kafka

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcredpanda "github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/twmb/franz-go/pkg/kgo"
)

// The image matrix of design §10: the last Kafka 3 (classic groups only),
// the latest Kafka 4 (classic and KIP-848 groups) and Redpanda.
const (
	kafka3   = "apache/kafka:3.9.2"
	kafka4   = "apache/kafka:4.3.1"
	redpanda = "redpandadata/redpanda:v26.2.1"
)

func ictx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

// startApache starts a single-node KRaft apache/kafka container and returns
// its broker address. The advertised listener needs the mapped port, so the
// container waits for a start script written after it is up.
func startApache(t *testing.T, image string) string {
	t.Helper()
	const script = "/tmp/sqlvim-start.sh"
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, image,
		testcontainers.WithExposedPorts("9092/tcp"),
		testcontainers.WithEnv(map[string]string{
			"KAFKA_NODE_ID":                                  "1",
			"KAFKA_PROCESS_ROLES":                            "broker,controller",
			"KAFKA_LISTENERS":                                "PLAINTEXT://0.0.0.0:9092,BROKER://0.0.0.0:19092,CONTROLLER://0.0.0.0:9093",
			"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP":           "PLAINTEXT:PLAINTEXT,BROKER:PLAINTEXT,CONTROLLER:PLAINTEXT",
			"KAFKA_INTER_BROKER_LISTENER_NAME":               "BROKER",
			"KAFKA_CONTROLLER_LISTENER_NAMES":                "CONTROLLER",
			"KAFKA_CONTROLLER_QUORUM_VOTERS":                 "1@localhost:9093",
			"KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR":         "1",
			"KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1",
			"KAFKA_TRANSACTION_STATE_LOG_MIN_ISR":            "1",
			"KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS":         "0",
			"KAFKA_AUTO_CREATE_TOPICS_ENABLE":                "true", // the client must still never create one
		}),
		testcontainers.WithEntrypoint("sh"),
		testcontainers.WithCmd("-c", "while [ ! -f "+script+" ]; do sleep 0.1; done; exec sh "+script),
		testcontainers.WithLifecycleHooks(testcontainers.ContainerLifecycleHooks{
			PostStarts: []testcontainers.ContainerHook{func(ctx context.Context, c testcontainers.Container) error {
				endpoint, err := c.PortEndpoint(ctx, "9092/tcp", "")
				if err != nil {
					return err
				}
				s := "export KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://" + endpoint + ",BROKER://localhost:19092\nexec /etc/kafka/docker/run\n"
				if err := c.CopyToContainer(ctx, []byte(s), script, 0o755); err != nil {
					return err
				}
				return wait.ForLog("Kafka Server started").WithStartupTimeout(2*time.Minute).WaitUntilReady(ctx, c)
			}},
		}),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "9092/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func startRedpanda(t *testing.T, opts ...testcontainers.ContainerCustomizer) string {
	t.Helper()
	ctx := context.Background()
	ctr, err := tcredpanda.Run(ctx, redpanda, opts...)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := ctr.KafkaSeedBroker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return broker
}

func TestIntegrationKafka3(t *testing.T) {
	coreSuite(t, Config{Brokers: []string{startApache(t, kafka3)}}, false)
}

func TestIntegrationKafka4(t *testing.T) {
	coreSuite(t, Config{Brokers: []string{startApache(t, kafka4)}}, true)
}

func TestIntegrationRedpanda(t *testing.T) {
	coreSuite(t, Config{Brokers: []string{startRedpanda(t)}}, false)
}

// unsupported skips a step the cluster does not support, so the run says
// which requests a distribution lacks.
func unsupported(t *testing.T, err error, step string) bool {
	if errors.Is(err, ErrUnsupported) {
		t.Logf("%s: %v", step, err)
		return true
	}
	return false
}

func coreSuite(t *testing.T, cfg Config, kip848 bool) {
	cl, err := Open(ictx(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ctx := ictx(t)
	info, err := cl.Cluster(ctx)
	if err != nil || len(info.Brokers) != 1 {
		t.Fatalf("cluster = %+v, %v", info, err)
	}
	t.Logf("version guess %q", info.Version)
	if err := cl.CreateTopic(ctx, NewTopic{Name: "orders", Partitions: 3, Replication: -1}); err != nil {
		t.Fatal(err)
	}
	topics, err := cl.Topics(ctx)
	if err != nil || !slices.ContainsFunc(topics, func(tp Topic) bool { return tp.Name == "orders" && tp.Partitions == 3 }) {
		t.Fatalf("topics = %+v, %v", topics, err)
	}

	t0 := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	var recs []ProduceRecord
	for i := range 10 {
		recs = append(recs, ProduceRecord{Key: []byte{'k', byte('0' + i)}, Value: []byte(`{"n":1}`), Partition: 0})
	}
	res, err := cl.Produce(ctx, "orders", recs)
	if err != nil || res[9].Offset != 9 || res[9].Partition != 0 {
		t.Fatalf("produce = %+v, %v", res, err)
	}
	if _, err := cl.Produce(ctx, "typo-topic", recs[:1]); err == nil {
		t.Fatal("produce to a missing topic")
	}
	if topics, _ := cl.Topics(ctx); slices.ContainsFunc(topics, func(tp Topic) bool { return tp.Name == "typo-topic" }) {
		t.Fatal("produce created a topic")
	}

	d, err := cl.TopicDetail(ctx, "orders")
	if err != nil || d.Parts[0].End != 10 || d.Parts[1].End != 0 {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	if _, err := cl.TopicConfigs(ctx, "orders"); err != nil && !unsupported(t, err, "TopicConfigs") {
		t.Fatal(err)
	}

	r, err := OpenReader(ctx, cfg, ReaderConfig{MaxValueBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := r.Range(ctx, "orders", map[int32]Range{0: {7, 10}})
	if err != nil || len(got) != 3 || got[0].Offset != 7 || string(got[0].Key) != "k7" {
		t.Fatalf("range = %+v, %v", got, err)
	}

	at, err := cl.OffsetsAt(ctx, "orders", t0)
	if err != nil && !unsupported(t, err, "OffsetsAt") {
		t.Fatal(err)
	} else if err == nil && (at[0] != 0 || at[1] != 0) {
		t.Fatalf("offsets at = %v", at)
	}

	res2, err := cl.ResetOffsets(ctx, "nobody", nil)
	if err == nil {
		t.Fatalf("reset of a missing group = %+v", res2)
	}
	// a group that only has committed offsets is Empty
	kcl, err := kgo.NewClient(kgo.SeedBrokers(cfg.Brokers...), kgo.ConsumerGroup("order-svc"), kgo.ConsumeTopics("orders"),
		kgo.DisableAutoCommit(), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 4; {
		fs := kcl.PollFetches(ctx)
		if err := fs.Err0(); err != nil {
			t.Fatal(err)
		}
		for _, rec := range fs.Records() {
			if rec.Partition == 0 && rec.Offset == 3 {
				if err := kcl.CommitRecords(ctx, rec); err != nil {
					t.Fatal(err)
				}
			}
			n++
		}
	}
	kcl.Close()
	gd, err := cl.GroupDetail(ctx, "order-svc")
	if err != nil {
		t.Fatal(err)
	}
	var p0 PartitionLag
	for _, l := range gd.Lag {
		if l.Partition == 0 {
			p0 = l
		}
	}
	if gd.State != "Empty" || p0.Committed != 4 || p0.Lag != 6 {
		t.Fatalf("group = %+v, p0 = %+v", gd.Group, p0)
	}
	cr, err := cl.ResetOffsets(ctx, "order-svc", []OffsetCommit{{TP: TP{"orders", 0}, Expect: 4, To: 5}})
	if err != nil || cr[0].Err != nil {
		t.Fatalf("reset = %+v, %v", cr, err)
	}
	if _, err := cl.ResetOffsets(ctx, "order-svc", []OffsetCommit{{TP: TP{"orders", 0}, Expect: 4, To: 6}}); !errors.Is(err, ErrGroupChanged) {
		t.Fatalf("stale preview: %v", err)
	}

	if kip848 {
		if !cl.Supports("consumer_groups") {
			t.Fatal("Kafka 4 supports KIP-848")
		}
		nk, err := kgo.NewClient(kgo.SeedBrokers(cfg.Brokers...), kgo.ConsumerGroup("nextgen"), kgo.ConsumeTopics("orders"),
			kgo.ServerSideBalancer())
		if err != nil {
			t.Fatal(err)
		}
		defer nk.Close()
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		nk.PollFetches(pctx)
		cancel()
		ng, err := cl.GroupDetail(ctx, "nextgen")
		if err != nil || ng.Type != GroupConsumer || len(ng.Members) != 1 || len(ng.Members[0].Assigned) != 3 {
			t.Fatalf("nextgen = %+v, %v", ng, err)
		}
		if _, err := cl.ResetOffsets(ctx, "nextgen", []OffsetCommit{{TP: TP{"orders", 0}, Expect: -1, To: 0}}); !errors.Is(err, ErrGroupNotEmpty) {
			t.Fatalf("reset of a live KIP-848 group: %v", err)
		}
	}

	if err := cl.DeleteTopic(ctx, "orders"); err != nil {
		t.Fatal(err)
	}
}

// selfSigned writes a CA-less self-signed cert for localhost and returns
// the PEM bytes and the cert file path.
func selfSigned(t *testing.T) (certPEM, keyPEM []byte, certFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})
	certFile = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM, certFile
}

// TestIntegrationSCRAMOverTLS logs in with SCRAM-SHA-256 over TLS, then
// with a wrong password, which must be ErrAuthFailed without the password
// in the message.
func TestIntegrationSCRAMOverTLS(t *testing.T) {
	certPEM, keyPEM, caFile := selfSigned(t)
	broker := startRedpanda(t,
		tcredpanda.WithEnableSASL(),
		tcredpanda.WithEnableKafkaAuthorization(),
		tcredpanda.WithNewServiceAccount("app", "right-pw-123"),
		tcredpanda.WithSuperusers("app"),
		tcredpanda.WithTLS(certPEM, keyPEM),
	)
	host, port, _ := net.SplitHostPort(broker)
	if host != "localhost" && net.ParseIP(host) == nil {
		t.Logf("broker host %q", host)
	}
	cfg := Config{Brokers: []string{net.JoinHostPort("localhost", port)}, SASL: "scram-sha-256", User: "app",
		Password: "right-pw-123", TLS: true, TLSCA: caFile}
	cl, err := Open(ictx(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Topics(ictx(t)); err != nil {
		t.Fatal(err)
	}
	cl.Close()
	cfg.Password = "wrong-pw-456"
	_, err = Open(ictx(t), cfg)
	if !errors.Is(err, ErrAuthFailed) || strings.Contains(err.Error(), "wrong-pw-456") {
		t.Fatalf("err = %v", err)
	}
	cfg.Password, cfg.TLSCA = "right-pw-123", ""
	if _, err := Open(ictx(t), cfg); err == nil {
		t.Fatal("an unknown CA must fail verification")
	}
}
