package kafka

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

func openReader(t *testing.T, c *kfake.Cluster, rc ReaderConfig) Reader {
	t.Helper()
	r, err := OpenReader(ctx(t), Config{Brokers: c.ListenAddrs()}, rc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func offsets(recs []Record) []int64 {
	var out []int64
	for _, r := range recs {
		out = append(out, r.Offset)
	}
	return out
}

func TestRange(t *testing.T) {
	c := newCluster(t)
	fill(t, c, "orders", 1, 100)
	recs, err := openReader(t, c, ReaderConfig{}).Range(ctx(t), "orders", map[int32]Range{0: {90, 100}})
	if err != nil {
		t.Fatal(err)
	}
	if got := offsets(recs); !slices.Equal(got, []int64{90, 91, 92, 93, 94, 95, 96, 97, 98, 99}) {
		t.Fatalf("offsets = %v", got)
	}
	if r := recs[0]; r.Topic != "orders" || r.Partition != 0 || r.TimeType != "CreateTime" || r.Time.IsZero() || r.Size != 1 {
		t.Fatalf("record = %+v", r)
	}
}

func TestRangeMultiPartition(t *testing.T) {
	c := newCluster(t)
	fill(t, c, "orders", 2, 20)
	recs, err := openReader(t, c, ReaderConfig{}).Range(ctx(t), "orders", map[int32]Range{1: {0, 5}, 0: {10, 15}})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 10 || recs[0].Partition != 0 || recs[0].Offset != 10 || recs[5].Partition != 1 || recs[5].Offset != 0 {
		t.Fatalf("records = %v", recs)
	}
}

func TestRangeGap(t *testing.T) {
	c := newCluster(t)
	tx, err := kgo.NewClient(kgo.SeedBrokers(c.ListenAddrs()...), kgo.TransactionalID("tx"),
		kgo.RecordPartitioner(kgo.ManualPartitioner()))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	for range 2 {
		if err := tx.BeginTransaction(); err != nil {
			t.Fatal(err)
		}
		if err := tx.ProduceSync(ctx(t), &kgo.Record{Topic: "orders", Value: []byte("a")}, &kgo.Record{Topic: "orders", Value: []byte("b")}).FirstErr(); err != nil {
			t.Fatal(err)
		}
		if err := tx.EndTransaction(ctx(t), kgo.TryCommit); err != nil {
			t.Fatal(err)
		}
	}
	// offsets 0,1 data, 2 commit marker, 3,4 data, 5 commit marker
	start := time.Now()
	recs, err := openReader(t, c, ReaderConfig{}).Range(ctx(t), "orders", map[int32]Range{0: {0, 6}})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if got := offsets(recs); !slices.Equal(got, []int64{0, 1, 3, 4}) {
		t.Fatalf("offsets = %v (control records must be left out)", got)
	}
}

func TestRangeOutOfRange(t *testing.T) {
	c := newCluster(t)
	fill(t, c, "orders", 1, 100)
	_, err := openReader(t, c, ReaderConfig{}).Range(ctx(t), "orders", map[int32]Range{0: {500, 600}})
	var oor ErrOutOfRange
	if !errors.As(err, &oor) || oor.Partition != 0 || oor.Start != 0 || oor.End != 100 {
		t.Fatalf("err = %#v", err)
	}
}

func TestRangeClampsToEnd(t *testing.T) {
	c := newCluster(t)
	fill(t, c, "orders", 1, 10)
	recs, err := openReader(t, c, ReaderConfig{}).Range(ctx(t), "orders", map[int32]Range{0: {5, 50}})
	if err != nil || len(recs) != 5 {
		t.Fatalf("got %d records, %v", len(recs), err)
	}
}

func TestTruncate(t *testing.T) {
	c := newCluster(t)
	produce(t, c, &kgo.Record{
		Topic: "orders", Key: []byte("k"), Value: bytes.Repeat([]byte("x"), 20),
		Headers: []kgo.RecordHeader{{Key: "h", Value: []byte("vv")}},
	})
	recs, err := openReader(t, c, ReaderConfig{MaxValueBytes: 10}).Range(ctx(t), "orders", map[int32]Range{0: {0, 1}})
	if err != nil {
		t.Fatal(err)
	}
	r := recs[0]
	if len(r.Value) != 10 || !r.Truncated || r.Size != 1+20+1+2 || string(r.Key) != "k" || len(r.Headers) != 1 {
		t.Fatalf("record = %+v", r)
	}
}

func TestNoGroupSideEffect(t *testing.T) {
	c := newCluster(t)
	fill(t, c, "orders", 3, 5)
	r := openReader(t, c, ReaderConfig{})
	if _, err := r.Range(ctx(t), "orders", map[int32]Range{0: {0, 5}, 1: {0, 5}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Tail(ctx(t), "orders", map[int32]int64{0: 5}); err != nil {
		t.Fatal(err)
	}
	pc, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := r.Poll(pc, 10); err != nil {
		t.Fatal(err)
	}
	groups, err := open(t, c).Groups(ctx(t))
	if err != nil || len(groups) != 0 {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
}

func TestTail(t *testing.T) {
	c := newCluster(t)
	fill(t, c, "orders", 3, 5)
	r := openReader(t, c, ReaderConfig{})
	if err := r.Tail(ctx(t), "orders", map[int32]int64{0: 5, 1: 5, 2: 5}); err != nil {
		t.Fatal(err)
	}
	produce(t, c, &kgo.Record{Topic: "orders", Partition: 0, Value: []byte("a")},
		&kgo.Record{Topic: "orders", Partition: 1, Value: []byte("b")},
		&kgo.Record{Topic: "orders", Partition: 2, Value: []byte("c")})
	var got []Record
	deadline := time.Now().Add(5 * time.Second)
	for len(got) < 3 && time.Now().Before(deadline) {
		pc, cancel := context.WithTimeout(context.Background(), time.Second)
		recs, err := r.Poll(pc, 500)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, recs...)
	}
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	for _, rec := range got {
		if rec.Offset != 5 {
			t.Fatalf("record = %+v", rec)
		}
	}
}
