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
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/twmb/franz-go/pkg/kadm"
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
func startApache(t *testing.T, image string, opts ...apacheOpts) string {
	t.Helper()
	const script = "/tmp/sqlvim-start.sh"
	var o apacheOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	brokerHost := "localhost"
	extra := []testcontainers.ContainerCustomizer{testcontainers.WithEnv(o.env)}
	if o.nw != nil {
		brokerHost = "kafka" // other containers on the network reach the BROKER listener here
		extra = append(extra, network.WithNetwork([]string{brokerHost}, o.nw))
	}
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, image, append([]testcontainers.ContainerCustomizer{
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
				s := "export KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://" + endpoint + ",BROKER://" + brokerHost + ":19092\nexec /etc/kafka/docker/run\n"
				if err := c.CopyToContainer(ctx, []byte(s), script, 0o755); err != nil {
					return err
				}
				return wait.ForLog("Kafka Server started").WithStartupTimeout(2*time.Minute).WaitUntilReady(ctx, c)
			}},
		}),
	}, extra...)...)
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

// apacheOpts are what a test adds to startApache's container: more
// environment (broker settings), and a network other containers join.
type apacheOpts struct {
	env map[string]string
	nw  *testcontainers.DockerNetwork
}

// withAuthorizer turns ACLs on; every PLAINTEXT client is ANONYMOUS, so
// that is the super user.
var withAuthorizer = apacheOpts{env: map[string]string{
	"KAFKA_AUTHORIZER_CLASS_NAME": "org.apache.kafka.metadata.authorizer.StandardAuthorizer",
	"KAFKA_SUPER_USERS":           "User:ANONYMOUS",
}}

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
	cfg := Config{Brokers: []string{startApache(t, kafka3)}}
	coreSuite(t, cfg, false)
	k2Suite(t, cfg)
}

func TestIntegrationKafka4(t *testing.T) {
	cfg := Config{Brokers: []string{startApache(t, kafka4)}}
	coreSuite(t, cfg, true)
	k2Suite(t, cfg)
}

func TestIntegrationRedpanda(t *testing.T) {
	cfg := Config{Brokers: []string{startRedpanda(t)}}
	coreSuite(t, cfg, false)
	k2Suite(t, cfg)
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

// alterConfig sets name (or, delete, removes its override) from what it is now.
func alterConfig(t *testing.T, cl Client, topic, name, value string, del bool) {
	t.Helper()
	e := configOf(t, cl, topic, name)
	ch := ConfigChange{Name: name, Value: value, Delete: del, Old: e.Value, OldDefault: e.Default}
	if err := cl.AlterTopicConfigs(ictx(t), topic, []ConfigChange{ch}); err != nil {
		t.Fatalf("alter %s: %v", name, err)
	}
}

// k2Suite runs the K2 admin requests: configs, partitions, deleting
// messages, groups and group offsets, and read_committed browsing.
func k2Suite(t *testing.T, cfg Config) {
	cl, err := Open(ictx(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ctx := ictx(t)
	if err := cl.CreateTopic(ctx, NewTopic{Name: "k2", Partitions: 3, Replication: -1}); err != nil {
		t.Fatal(err)
	}

	// configs: changing one leaves the other override alone; delete goes back to the default
	alterConfig(t, cl, "k2", "retention.ms", "3600000", false)
	alterConfig(t, cl, "k2", "max.message.bytes", "2000000", false)
	alterConfig(t, cl, "k2", "retention.ms", "7200000", false)
	if e := configOf(t, cl, "k2", "max.message.bytes"); e.Value != "2000000" || e.Default {
		t.Fatalf("max.message.bytes after another change = %+v", e)
	}
	if e := configOf(t, cl, "k2", "retention.ms"); e.Value != "7200000" {
		t.Fatalf("retention.ms = %+v", e)
	}
	alterConfig(t, cl, "k2", "retention.ms", "", true)
	if e := configOf(t, cl, "k2", "retention.ms"); !e.Default {
		t.Fatalf("retention.ms after delete = %+v", e)
	}
	if e := configOf(t, cl, "k2", "max.message.bytes"); e.Value != "2000000" {
		t.Fatalf("max.message.bytes after a delete = %+v", e)
	}
	stale := ConfigChange{Name: "max.message.bytes", Value: "3000000", Old: "1", OldDefault: false}
	if err := cl.AlterTopicConfigs(ctx, "k2", []ConfigChange{stale}); !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("stale config change: %v", err)
	}

	if err := cl.AddPartitions(ctx, "k2", 5); err != nil {
		t.Fatal(err)
	}
	// new partitions elect their leaders for a moment (LEADER_NOT_AVAILABLE on Redpanda)
	var d TopicDetail
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if d, err = cl.TopicDetail(ctx, "k2"); err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err != nil || len(d.Parts) != 5 {
		t.Fatalf("detail after adding partitions = %+v, %v", d, err)
	}
	if err := cl.AddPartitions(ctx, "k2", 4); err == nil {
		t.Fatal("partitions shrank")
	}

	var recs []ProduceRecord
	for i := range 10 {
		recs = append(recs, ProduceRecord{Value: []byte{'v', byte('0' + i)}, Partition: 0})
	}
	if _, err := cl.Produce(ctx, "k2", recs); err != nil {
		t.Fatal(err)
	}
	dr, err := cl.DeleteRecords(ctx, "k2", map[int32]int64{0: 4})
	if !cl.Supports("delete_records") {
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("delete records without support: %v", err)
		}
		t.Logf("DeleteRecords: %v", err)
	} else {
		if err != nil || len(dr) != 1 || dr[0].Err != nil || dr[0].Start != 4 {
			t.Fatalf("delete records = %+v, %v", dr, err)
		}
		if d, err := cl.TopicDetail(ctx, "k2"); err != nil || d.Parts[0].Start != 4 {
			t.Fatalf("detail after delete = %+v, %v", d, err)
		}
		var oor ErrOutOfRange
		if _, err := cl.DeleteRecords(ctx, "k2", map[int32]int64{0: 11}); !errors.As(err, &oor) {
			t.Fatalf("delete past the end: %v", err)
		}
	}

	// a live consumer: neither the group nor its offsets on k2 may go
	kcl, err := kgo.NewClient(kgo.SeedBrokers(cfg.Brokers...), kgo.ConsumerGroup("k2-svc"), kgo.ConsumeTopics("k2"),
		kgo.DisableAutoCommit(), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n == 0; {
		fs := kcl.PollFetches(ctx)
		if err := fs.Err0(); err != nil {
			t.Fatal(err)
		}
		n = fs.NumRecords()
	}
	if err := kcl.CommitUncommittedOffsets(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cl.DeleteGroup(ctx, "k2-svc"); !errors.Is(err, ErrGroupNotEmpty) {
		t.Fatalf("delete of a live group: %v", err)
	}
	if _, err := cl.DeleteGroupOffsets(ctx, "k2-svc", "k2"); !errors.Is(err, ErrGroupSubscribed) {
		t.Fatalf("delete of offsets the group consumes: %v", err)
	}
	kcl.Close()
	// offsets on a second topic keep the group alive once those on k2 are
	// gone (Kafka 3 and Redpanda drop an Empty group without offsets)
	if err := cl.CreateTopic(ctx, NewTopic{Name: "k2-tx", Partitions: 1, Replication: -1}); err != nil {
		t.Fatal(err)
	}
	akcl, err := kgo.NewClient(kgo.SeedBrokers(cfg.Brokers...))
	if err != nil {
		t.Fatal(err)
	}
	var offs kadm.Offsets
	offs.Add(kadm.Offset{Topic: "k2-tx", Partition: 0, At: 0, LeaderEpoch: -1})
	if err := kadm.NewClient(akcl).CommitAllOffsets(ctx, "k2-svc", offs); err != nil {
		t.Fatal(err)
	}
	akcl.Close()
	if res, err := cl.DeleteGroupOffsets(ctx, "k2-svc", "k2"); err != nil || len(res) == 0 || res[0].Err != nil {
		t.Fatalf("delete offsets of an empty group = %+v, %v", res, err)
	}
	if err := cl.DeleteGroup(ctx, "k2-svc"); err != nil {
		t.Fatal(err)
	}
	if gs, err := cl.Groups(ctx); err != nil || slices.ContainsFunc(gs, func(g Group) bool { return g.Name == "k2-svc" }) {
		t.Fatalf("groups after delete = %+v, %v", gs, err)
	}

	// an aborted and a committed transaction: read_committed shows the second only
	tx, err := kgo.NewClient(kgo.SeedBrokers(cfg.Brokers...), kgo.TransactionalID("sqlvim-it"), kgo.DefaultProduceTopic("k2-tx"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"aborted", "committed"} {
		if err := tx.BeginTransaction(); err != nil {
			t.Fatal(err)
		}
		if err := tx.ProduceSync(ctx, &kgo.Record{Value: []byte(v)}).FirstErr(); err != nil {
			t.Fatal(err)
		}
		if err := tx.EndTransaction(ctx, kgo.TransactionEndTry(v == "committed")); err != nil {
			t.Fatal(err)
		}
	}
	tx.Close()
	values := func(rc ReaderConfig) []string {
		r, err := OpenReader(ctx, cfg, rc)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		got, err := r.Range(ctx, "k2-tx", map[int32]Range{0: {0, 100}})
		if err != nil {
			t.Fatal(err)
		}
		var vs []string
		for _, rec := range got {
			vs = append(vs, string(rec.Value))
		}
		return vs
	}
	if vs := values(ReaderConfig{}); !slices.Equal(vs, []string{"committed"}) {
		t.Fatalf("read_committed = %q", vs)
	}
	if vs := values(ReaderConfig{ReadUncommitted: true}); !slices.Equal(vs, []string{"aborted", "committed"}) {
		t.Fatalf("read_uncommitted = %q", vs)
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
	cfg := Config{
		Brokers: []string{net.JoinHostPort("localhost", port)}, SASL: "scram-sha-256", User: "app",
		Password: "right-pw-123", TLS: true, TLSCA: caFile,
	}
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
