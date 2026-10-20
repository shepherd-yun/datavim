package kafka

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// newCluster starts a 3-broker kfake cluster with orders (3 partitions).
func newCluster(t *testing.T, opts ...kfake.Opt) *kfake.Cluster {
	t.Helper()
	c, err := kfake.NewCluster(append([]kfake.Opt{kfake.NumBrokers(3), kfake.SeedTopics(3, "orders")}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func open(t *testing.T, c *kfake.Cluster) Client {
	t.Helper()
	cl, err := Open(ctx(t), Config{Brokers: c.ListenAddrs(), ClientID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cl.Close)
	return cl
}

// produce writes recs synchronously with a plain kgo client.
func produce(t *testing.T, c *kfake.Cluster, recs ...*kgo.Record) {
	t.Helper()
	p, err := kgo.NewClient(kgo.SeedBrokers(c.ListenAddrs()...), kgo.RecordPartitioner(kgo.ManualPartitioner()))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.ProduceSync(ctx(t), recs...).FirstErr(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAndCluster(t *testing.T) {
	c := newCluster(t)
	info, err := open(t, c).Cluster(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Brokers) != 3 || info.ID == "" {
		t.Fatalf("cluster = %+v", info)
	}
	n := 0
	for _, b := range info.Brokers {
		if b.Controller {
			n++
			if b.ID != info.Controller {
				t.Fatalf("controller %d marked on broker %d", info.Controller, b.ID)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d controllers: %+v", n, info.Brokers)
	}
	leaders := 0
	for _, b := range info.Brokers {
		leaders += b.Leaders
	}
	if leaders < 3 {
		t.Fatalf("leaders = %d, want ≥ 3 (orders has 3 partitions)", leaders)
	}
}

func TestTopics(t *testing.T) {
	c := newCluster(t, kfake.SeedTopics(1, "refunds"))
	cl := open(t, c)
	topics, err := cl.Topics(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tp := range topics {
		names = append(names, tp.Name)
		if tp.Name == "orders" && (tp.Partitions != 3 || tp.Replication < 1) {
			t.Fatalf("orders = %+v", tp)
		}
		if strings.HasPrefix(tp.Name, "__") && !tp.Internal {
			t.Fatalf("%s is internal", tp.Name)
		}
	}
	if !slices.IsSorted(names) || !slices.Contains(names, "orders") || !slices.Contains(names, "refunds") {
		t.Fatalf("names = %v", names)
	}
}

func TestTopicDetail(t *testing.T) {
	c := newCluster(t)
	for i := range 5 {
		produce(t, c, &kgo.Record{Topic: "orders", Partition: 0, Value: []byte{byte(i)}})
	}
	d, err := open(t, c).TopicDetail(ctx(t), "orders")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "orders" || len(d.Parts) != 3 {
		t.Fatalf("detail = %+v", d)
	}
	p0 := d.Parts[0]
	if p0.ID != 0 || p0.Start != 0 || p0.End != 5 || !slices.Contains(p0.Replicas, p0.Leader) {
		t.Fatalf("p0 = %+v", p0)
	}
}

func TestTopicConfigs(t *testing.T) {
	c := newCluster(t)
	cfgs, err := open(t, c).TopicConfigs(ctx(t), "orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) == 0 || !slices.IsSortedFunc(cfgs, func(a, b ConfigEntry) int { return strings.Compare(a.Name, b.Name) }) {
		t.Fatalf("configs = %+v", cfgs)
	}
}

func TestDialerUsed(t *testing.T) {
	c := newCluster(t)
	var mu sync.Mutex
	dialed := map[string]bool{}
	cfg := Config{Brokers: c.ListenAddrs()[:1], Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialed[addr] = true
		mu.Unlock()
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
	cl, err := Open(ctx(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if _, err := cl.Topics(ctx(t)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, a := range c.ListenAddrs() {
		if !dialed[a] {
			t.Errorf("%s was not dialed through Config.Dial: %v", a, dialed)
		}
	}
}

func TestNoAutoCreate(t *testing.T) {
	// the cluster would create topics; the client must never ask it to
	c := newCluster(t, kfake.AllowAutoTopicCreation())
	cl := open(t, c)
	if _, err := cl.TopicDetail(ctx(t), "nope"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
	if _, err := cl.TopicConfigs(ctx(t), "nope2"); err == nil {
		t.Fatal("configs of a missing topic")
	}
	topics, err := cl.Topics(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range topics {
		if strings.HasPrefix(tp.Name, "nope") {
			t.Fatalf("%s was created", tp.Name)
		}
	}
}

func TestSASLScram(t *testing.T) {
	c := newCluster(t, kfake.EnableSASL(), kfake.Superuser("SCRAM-SHA-256", "app", "s3cret-pw"))
	cfg := Config{Brokers: c.ListenAddrs(), SASL: "scram-sha-256", User: "app", Password: "s3cret-pw"}
	cl, err := Open(ctx(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	cl.Close()
	// kfake drops the connection on a wrong password; a real broker answers
	// SASL_AUTHENTICATION_FAILED, so answer like one
	c.ControlKey(int16(kmsg.SASLAuthenticate), func(r kmsg.Request) (kmsg.Response, error, bool) {
		c.KeepControl()
		resp := r.ResponseKind().(*kmsg.SASLAuthenticateResponse)
		resp.SetVersion(r.GetVersion())
		resp.ErrorCode = kerr.SaslAuthenticationFailed.Code
		msg := "Authentication failed during authentication due to invalid credentials with SASL mechanism SCRAM-SHA-256"
		resp.ErrorMessage = &msg
		return resp, nil, true
	})
	cfg.Password = "wrong-pw-xyz"
	_, err = Open(ctx(t), cfg)
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
	if strings.Contains(err.Error(), "wrong-pw-xyz") || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("password in %q", err)
	}
}

func TestSASLMechanismNotEnabled(t *testing.T) {
	c := newCluster(t, kfake.EnableSASL(), kfake.Superuser("SCRAM-SHA-256", "app", "s3cret-pw"))
	// kfake drops the connection; a real broker answers the handshake
	c.ControlKey(int16(kmsg.SASLHandshake), func(r kmsg.Request) (kmsg.Response, error, bool) {
		c.KeepControl()
		resp := r.ResponseKind().(*kmsg.SASLHandshakeResponse)
		resp.SetVersion(r.GetVersion())
		resp.ErrorCode = kerr.UnsupportedSaslMechanism.Code
		resp.SupportedMechanisms = []string{"SCRAM-SHA-256"}
		return resp, nil, true
	})
	_, err := Open(ctx(t), Config{Brokers: c.ListenAddrs(), SASL: "plain", User: "app", Password: "s3cret-pw"})
	// not a wrong password: asking for it again would never help
	if err == nil || errors.Is(err, ErrAuthFailed) || !strings.Contains(err.Error(), `sasl "plain"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestSupports(t *testing.T) {
	cl := open(t, newCluster(t))
	if !cl.Supports("offsets_by_time") || !cl.Supports("delete_records") {
		t.Fatal("kfake supports ListOffsets v1 and DeleteRecords")
	}
	if cl.Supports("nonsense") {
		t.Fatal("unknown feature")
	}
}

func TestConnLost(t *testing.T) {
	c := newCluster(t)
	cl := open(t, c)
	c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := cl.Topics(ctx)
	if err == nil {
		t.Fatal("no error after the cluster closed")
	}
}

func TestFormatVersionGuess(t *testing.T) {
	for in, want := range map[string]string{
		"v3.9":                     "3.9",
		"at least v4.1":            "≥ 4.1",
		"between v0.11.0 and v1.0": "unknown",
		"unknown custom version":   "unknown",
		"":                         "unknown",
	} {
		if got := formatVersionGuess(in); got != want {
			t.Errorf("formatVersionGuess(%q) = %q, want %q", in, got, want)
		}
	}
}
