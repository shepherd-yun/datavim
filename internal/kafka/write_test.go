package kafka

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

func TestProduce(t *testing.T) {
	c := newCluster(t)
	cl := open(t, c)
	res, err := cl.Produce(ctx(t), "orders", []ProduceRecord{
		{Key: []byte("k1"), Value: []byte("a"), Partition: 2},
		{Key: []byte("k1"), Value: []byte("b"), Partition: 2},
		{Key: []byte("same"), Value: []byte("c"), Partition: -1},
		{Key: []byte("same"), Value: []byte("d"), Partition: -1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 4 {
		t.Fatalf("results = %+v", res)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	if res[0].Partition != 2 || res[0].Offset != 0 || res[1].Offset != 1 || res[0].Time.IsZero() {
		t.Fatalf("results = %+v", res)
	}
	if res[2].Partition != res[3].Partition || res[3].Offset != res[2].Offset+1 {
		t.Fatalf("one key goes to one partition: %+v", res)
	}
	if _, err := cl.Produce(ctx(t), "orders", []ProduceRecord{{Value: []byte("x"), Partition: 9}}); err == nil ||
		!strings.Contains(err.Error(), "partition 9") {
		t.Fatalf("err = %v", err)
	}
}

func TestProduceMissingTopic(t *testing.T) {
	c := newCluster(t, kfake.AllowAutoTopicCreation())
	cl := open(t, c)
	_, err := cl.Produce(ctx(t), "typo", []ProduceRecord{{Value: []byte("x"), Partition: -1}})
	if err == nil || !strings.Contains(err.Error(), "typo doesn't exist") {
		t.Fatalf("err = %v", err)
	}
	topics, _ := cl.Topics(ctx(t))
	for _, tp := range topics {
		if tp.Name == "typo" {
			t.Fatal("producing created the topic")
		}
	}
}

func TestProduceNullAndHeaders(t *testing.T) {
	c := newCluster(t)
	cl := open(t, c)
	hs := []Header{{Key: "a", Value: []byte("1")}, {Key: "a", Value: []byte("2")}, {Key: "b", Value: []byte{}}}
	if _, err := cl.Produce(ctx(t), "orders", []ProduceRecord{
		{KeyNull: true, ValueNull: true, Partition: 0, Headers: hs},
		{Key: []byte{}, Value: []byte{}, Partition: 0},
	}); err != nil {
		t.Fatal(err)
	}
	recs, err := openReader(t, c, ReaderConfig{}).Range(ctx(t), "orders", map[int32]Range{0: {0, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if r := recs[0]; !r.KeyNull || !r.ValueNull || len(r.Headers) != 3 || r.Headers[1].Key != "a" || string(r.Headers[1].Value) != "2" {
		t.Fatalf("null record = %+v", r)
	}
	if r := recs[1]; r.KeyNull || r.ValueNull {
		t.Fatalf("empty record = %+v", r)
	}
}

func TestCreateDeleteTopic(t *testing.T) {
	c := newCluster(t)
	cl := open(t, c)
	if err := cl.CreateTopic(ctx(t), NewTopic{
		Name: "audit", Partitions: 4, Replication: -1,
		Configs: map[string]string{"retention.ms": "60000"},
	}); err != nil {
		t.Fatal(err)
	}
	d, err := cl.TopicDetail(ctx(t), "audit")
	if err != nil || len(d.Parts) != 4 {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	cfgs, _ := cl.TopicConfigs(ctx(t), "audit")
	i := slices.IndexFunc(cfgs, func(e ConfigEntry) bool { return e.Name == "retention.ms" })
	if i < 0 || cfgs[i].Value != "60000" {
		t.Fatalf("configs = %+v", cfgs)
	}
	if err := cl.CreateTopic(ctx(t), NewTopic{Name: "audit", Partitions: 1, Replication: -1}); err == nil {
		t.Fatal("creating it twice")
	}
	if err := cl.DeleteTopic(ctx(t), "audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.TopicDetail(ctx(t), "audit"); err == nil {
		t.Fatal("topic still there")
	}
}

func committedOf(t *testing.T, cl Client, group string, p int32) int64 {
	t.Helper()
	d, err := cl.GroupDetail(ctx(t), group)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range d.Lag {
		if l.Partition == p && l.Topic == "orders" {
			return l.Committed
		}
	}
	t.Fatalf("no lag row for p%d: %+v", p, d.Lag)
	return 0
}

func TestResetOffsets(t *testing.T) {
	c := newCluster(t)
	fill(t, c, "orders", 3, 10)
	commit(t, c, "svc", kadm.Offset{Topic: "orders", Partition: 0, At: 4, LeaderEpoch: -1})
	cl := open(t, c)
	res, err := cl.ResetOffsets(ctx(t), "svc", []OffsetCommit{
		{TP: TP{"orders", 0}, Expect: 4, To: 5},
		{TP: TP{"orders", 1}, Expect: -1, To: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Err != nil || res[1].Err != nil {
		t.Fatalf("results = %+v", res)
	}
	if got := committedOf(t, cl, "svc", 0); got != 5 {
		t.Fatalf("p0 committed = %d", got)
	}
	if got := committedOf(t, cl, "svc", 2); got != -1 {
		t.Fatalf("p2 was not in the reset: %d", got)
	}
}

func TestResetOffsetsNotEmpty(t *testing.T) {
	c := newCluster(t)
	fill(t, c, "orders", 3, 10)
	kcl := consume(t, c, "live", "orders", kgo.DisableAutoCommit())
	if err := kcl.CommitRecords(ctx(t), &kgo.Record{Topic: "orders", Partition: 0, Offset: 3}); err != nil {
		t.Fatal(err)
	}
	cl := open(t, c)
	_, err := cl.ResetOffsets(ctx(t), "live", []OffsetCommit{{TP: TP{"orders", 0}, Expect: 4, To: 9}})
	if !errors.Is(err, ErrGroupNotEmpty) {
		t.Fatalf("err = %v", err)
	}
	if got := committedOf(t, cl, "live", 0); got != 4 {
		t.Fatalf("committed = %d, want 4 (unchanged)", got)
	}
}

func TestResetOffsetsChanged(t *testing.T) {
	c := newCluster(t)
	fill(t, c, "orders", 3, 10)
	commit(t, c, "svc", kadm.Offset{Topic: "orders", Partition: 0, At: 6, LeaderEpoch: -1})
	cl := open(t, c)
	_, err := cl.ResetOffsets(ctx(t), "svc", []OffsetCommit{{TP: TP{"orders", 0}, Expect: 4, To: 5}})
	if !errors.Is(err, ErrGroupChanged) {
		t.Fatalf("err = %v", err)
	}
	if got := committedOf(t, cl, "svc", 0); got != 6 {
		t.Fatalf("committed = %d, want 6", got)
	}
}

func TestResetOffsetsShareGroupRefused(t *testing.T) {
	if got := resetRefusal(GroupShare); got == nil || got.Error() != "can't reset offsets of a share group" {
		t.Fatalf("share: %v", got)
	}
	for _, typ := range []GroupType{GroupConnect, GroupUnknown} {
		if resetRefusal(typ) == nil {
			t.Fatalf("%s groups are refused", typ)
		}
	}
	for _, typ := range []GroupType{GroupClassic, GroupConsumer} {
		if err := resetRefusal(typ); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
	}
}

func TestProduceWithoutIdempotentWriteACL(t *testing.T) {
	c := newCluster(t)
	// before Kafka 3.0 an idempotent producer needs the IDEMPOTENT_WRITE
	// cluster ACL, which a user allowed to write a topic may not have
	c.ControlKey(int16(kmsg.InitProducerID), func(r kmsg.Request) (kmsg.Response, error, bool) {
		c.KeepControl()
		resp := r.ResponseKind().(*kmsg.InitProducerIDResponse)
		resp.SetVersion(r.GetVersion())
		resp.ErrorCode = kerr.ClusterAuthorizationFailed.Code
		return resp, nil, true
	})
	cl := open(t, c)
	res, err := cl.Produce(ctx(t), "orders", []ProduceRecord{{Value: []byte("a"), Partition: 0}})
	if err != nil || len(res) != 1 || res[0].Err != nil || res[0].Offset != 0 {
		t.Fatalf("res = %+v, err %v", res, err)
	}
}
