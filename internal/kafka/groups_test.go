package kafka

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

// fill writes n records to each partition of orders.
func fill(t *testing.T, c *kfake.Cluster, topic string, parts int32, n int) {
	t.Helper()
	var recs []*kgo.Record
	for p := range parts {
		for i := range n {
			recs = append(recs, &kgo.Record{Topic: topic, Partition: p, Value: []byte{byte(i)}})
		}
	}
	produce(t, c, recs...)
}

// commit commits offsets for group as an admin would.
func commit(t *testing.T, c *kfake.Cluster, group string, offs ...kadm.Offset) {
	t.Helper()
	kcl, err := kgo.NewClient(kgo.SeedBrokers(c.ListenAddrs()...))
	if err != nil {
		t.Fatal(err)
	}
	defer kcl.Close()
	var os kadm.Offsets
	for _, o := range offs {
		os.Add(o)
	}
	if err := kadm.NewClient(kcl).CommitAllOffsets(ctx(t), group, os); err != nil {
		t.Fatal(err)
	}
}

// consume starts a group member on topic and waits until it has an
// assignment; the returned client is closed by t.Cleanup.
func consume(t *testing.T, c *kfake.Cluster, group, topic string, opts ...kgo.Opt) *kgo.Client {
	t.Helper()
	assigned := make(chan struct{}, 1)
	opts = append([]kgo.Opt{kgo.SeedBrokers(c.ListenAddrs()...), kgo.ConsumerGroup(group), kgo.ConsumeTopics(topic),
		kgo.ClientID("worker-1"),
		kgo.OnPartitionsAssigned(func(_ context.Context, _ *kgo.Client, m map[string][]int32) {
			if len(m) > 0 {
				select {
				case assigned <- struct{}{}:
				default:
				}
			}
		})}, opts...)
	kcl, err := kgo.NewClient(opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(kcl.Close)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			fs := kcl.PollFetches(context.Background())
			if fs.IsClientClosed() {
				return
			}
		}
	}()
	t.Cleanup(func() { kcl.Close(); <-done })
	select {
	case <-assigned:
	case <-time.After(10 * time.Second):
		t.Fatal("no assignment")
	}
	return kcl
}

func TestGroupDetailClassic(t *testing.T) {
	c := newCluster(t)
	fill(t, c, "orders", 3, 10)
	commit(t, c, "order-svc", kadm.Offset{Topic: "orders", Partition: 0, At: 4, LeaderEpoch: -1})
	d, err := open(t, c).GroupDetail(ctx(t), "order-svc")
	if err != nil {
		t.Fatal(err)
	}
	if d.State != "Empty" || d.Type != GroupClassic || len(d.Members) != 0 {
		t.Fatalf("group = %+v", d.Group)
	}
	if len(d.Lag) != 3 {
		t.Fatalf("lag = %+v", d.Lag)
	}
	byPart := map[int32]PartitionLag{}
	for _, l := range d.Lag {
		byPart[l.Partition] = l
	}
	if p0 := byPart[0]; p0.Committed != 4 || p0.Lag != 6 || p0.End != 10 {
		t.Fatalf("p0 = %+v", p0)
	}
	if p1 := byPart[1]; p1.Committed != -1 || p1.Lag != 10 {
		t.Fatalf("p1 = %+v", p1)
	}
	if !slices.IsSortedFunc(d.Lag, func(a, b PartitionLag) int { return int(b.Lag - a.Lag) }) {
		t.Fatalf("lag is not sorted by lag: %+v", d.Lag)
	}
}

func TestGroupDetailActive(t *testing.T) {
	c := newCluster(t)
	consume(t, c, "live", "orders")
	cl := open(t, c)
	d, err := cl.GroupDetail(ctx(t), "live")
	if err != nil {
		t.Fatal(err)
	}
	if d.State != "Stable" || len(d.Members) != 1 || d.Group.Members != 1 || d.Type != GroupClassic {
		t.Fatalf("group = %+v, members %+v", d.Group, d.Members)
	}
	m := d.Members[0]
	if m.ClientID != "worker-1" || !slices.Contains(m.Assigned, TP{Topic: "orders", Partition: 0}) {
		t.Fatalf("member = %+v", m)
	}
	for _, l := range d.Lag {
		if l.Member != "worker-1" {
			t.Fatalf("lag row without member: %+v", l)
		}
	}
	groups, err := cl.Groups(ctx(t))
	if err != nil || len(groups) != 1 || groups[0].Name != "live" || groups[0].State != "Stable" {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
}

func TestConsumerProtocolGroup(t *testing.T) {
	c := newCluster(t)
	fill(t, c, "orders", 3, 5)
	consume(t, c, "nextgen", "orders", kgo.ServerSideBalancer())
	cl := open(t, c)
	if !cl.Supports("consumer_groups") {
		t.Skip("kfake does not support KIP-848")
	}
	groups, err := cl.Groups(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Type != GroupConsumer {
		t.Fatalf("groups = %+v", groups)
	}
	d, err := cl.GroupDetail(ctx(t), "nextgen")
	if err != nil {
		t.Fatal(err)
	}
	if d.Type != GroupConsumer || len(d.Members) != 1 || len(d.Members[0].Assigned) != 3 || len(d.Lag) != 3 {
		t.Fatalf("detail = %+v", d)
	}
}

func TestGroupsLag(t *testing.T) {
	c := newCluster(t, kfake.SeedTopics(1, "refunds"))
	fill(t, c, "orders", 3, 10)
	fill(t, c, "refunds", 1, 7)
	commit(t, c, "a", kadm.Offset{Topic: "orders", Partition: 0, At: 4, LeaderEpoch: -1},
		kadm.Offset{Topic: "orders", Partition: 1, At: 10, LeaderEpoch: -1})
	commit(t, c, "b", kadm.Offset{Topic: "refunds", Partition: 0, At: 2, LeaderEpoch: -1})
	lag, err := open(t, c).GroupsLag(ctx(t), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	// a: p0 6 + p1 0 + p2 never committed 10; b: 5
	if lag["a"] != 16 || lag["b"] != 5 || len(lag) != 2 {
		t.Fatalf("lag = %v", lag)
	}
}

func TestTopicGroups(t *testing.T) {
	c := newCluster(t, kfake.SeedTopics(1, "refunds"))
	fill(t, c, "orders", 3, 10)
	commit(t, c, "a", kadm.Offset{Topic: "orders", Partition: 0, At: 4, LeaderEpoch: -1})
	commit(t, c, "b", kadm.Offset{Topic: "refunds", Partition: 0, At: 0, LeaderEpoch: -1})
	gs, err := open(t, c).TopicGroups(ctx(t), "orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || gs[0].Name != "a" || len(gs[0].Lag) != 3 {
		t.Fatalf("groups = %+v", gs)
	}
	for _, l := range gs[0].Lag {
		if l.Topic != "orders" {
			t.Fatalf("lag of another topic: %+v", l)
		}
	}
}

func TestOffsetsAt(t *testing.T) {
	c := newCluster(t)
	t0 := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	produce(t, c,
		&kgo.Record{Topic: "orders", Partition: 0, Timestamp: t0, Value: []byte("a")},
		&kgo.Record{Topic: "orders", Partition: 0, Timestamp: t0.Add(time.Minute), Value: []byte("b")},
		&kgo.Record{Topic: "orders", Partition: 0, Timestamp: t0.Add(2 * time.Minute), Value: []byte("c")})
	cl := open(t, c)
	got, err := cl.OffsetsAt(ctx(t), "orders", t0.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 1 || len(got) != 3 {
		t.Fatalf("offsets = %v", got)
	}
	got, err = cl.OffsetsAt(ctx(t), "orders", t0.Add(time.Hour))
	if err != nil || got[0] != 3 || got[1] != 0 {
		t.Fatalf("after the last record: %v, %v", got, err)
	}
}
