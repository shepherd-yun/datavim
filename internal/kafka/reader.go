package kafka

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// ReaderConfig tunes a Reader.
type ReaderConfig struct {
	// MaxValueBytes cuts longer values to this many bytes and sets
	// Record.Truncated; 0 keeps them whole.
	MaxValueBytes int
}

// ErrOutOfRange is a read of offsets the partition does not have.
type ErrOutOfRange struct {
	Partition  int32
	Start, End int64
}

func (e ErrOutOfRange) Error() string {
	return fmt.Sprintf("p%d has offsets %d to %d only", e.Partition, e.Start, e.End)
}

// Reader browses messages, one per message view. It never joins a group,
// never commits and has no method that writes. Not safe for concurrent use.
type Reader interface {
	// Range reads [From, To) of each partition (To is cut to the end),
	// stopping at To or the partition end even where offsets are missing
	// (compaction, transaction markers). Records are sorted by partition,
	// then offset.
	Range(ctx context.Context, topic string, ranges map[int32]Range) ([]Record, error)
	// Tail starts reading from the given offsets and keeps reading; Poll
	// returns what arrived.
	Tail(ctx context.Context, topic string, from map[int32]int64) error
	// Poll waits until there are records or ctx ends (not an error) and
	// returns at most max of them.
	Poll(ctx context.Context, maxRecs int) ([]Record, error)
	Close() error
}

type reader struct {
	cfg  Config
	rc   ReaderConfig
	mu   sync.Mutex
	tail *kgo.Client // the live tail's client, nil when not tailing
}

// OpenReader checks cfg once (an ApiVersions round) and returns a Reader.
// Every Range and Tail uses its own kgo client that only consumes the
// partitions it was given: there is no buffered data from an earlier read
// to mix in.
func OpenReader(ctx context.Context, cfg Config, rc ReaderConfig) (Reader, error) {
	c, err := Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	c.Close()
	return &reader{cfg: cfg, rc: rc}, nil
}

// consumer is a kgo client reading exactly the given offsets: no group,
// out-of-range offsets are errors (never a silent jump), control records
// are kept so that their offsets count as read.
func (r *reader) consumer(topic string, from map[int32]int64) (*kgo.Client, error) {
	opts, err := clientOpts(r.cfg)
	if err != nil {
		return nil, err
	}
	ps := make(map[int32]kgo.Offset, len(from))
	for p, o := range from {
		ps[p] = kgo.NewOffset().At(o)
	}
	opts = append(opts,
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: ps}),
		kgo.ConsumeResetOffset(kgo.NoResetOffset()),
		kgo.KeepControlRecords(),
	)
	return kgo.NewClient(opts...)
}

func (r *reader) stopTail() {
	if r.tail != nil {
		r.tail.Close()
		r.tail = nil
	}
}

func (r *reader) Range(ctx context.Context, topic string, ranges map[int32]Range) ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopTail()
	what := "topic " + topic
	if len(ranges) == 0 {
		return nil, nil
	}
	kcl, err := r.consumer(topic, nil)
	if err != nil {
		return nil, err
	}
	starts, ends, err := partitionBounds(ctx, kadm.NewClient(kcl), topic)
	kcl.Close()
	if err != nil {
		return nil, wrap(ctx, err, what)
	}
	want := map[int32]Range{}
	from := map[int32]int64{}
	for p, rg := range ranges {
		end, ok := ends[p]
		if !ok {
			return nil, fmt.Errorf("partition %d of %s doesn't exist", p, topic)
		}
		if rg.From < starts[p] || rg.From > end {
			return nil, ErrOutOfRange{Partition: p, Start: starts[p], End: end}
		}
		rg.To = min(rg.To, end)
		if rg.From < rg.To {
			want[p] = rg
			from[p] = rg.From
		}
	}
	if len(want) == 0 {
		return nil, nil
	}
	kcl, err = r.consumer(topic, from)
	if err != nil {
		return nil, err
	}
	defer kcl.Close()
	var out []Record
	seen := map[int32]int64{} // the highest offset read, control records too
	for p, rg := range want {
		seen[p] = rg.From - 1
	}
	done := func() bool {
		for p, rg := range want {
			if seen[p] < rg.To-1 {
				return false
			}
		}
		return true
	}
	for !done() {
		fs := kcl.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err := fetchErr(fs, topic); err != nil {
			return nil, wrap(ctx, err, what)
		}
		for _, kr := range fs.Records() {
			rg, ok := want[kr.Partition]
			if !ok || kr.Topic != topic || kr.Offset < rg.From || kr.Offset <= seen[kr.Partition] {
				continue
			}
			seen[kr.Partition] = kr.Offset
			if kr.Offset < rg.To && !kr.Attrs.IsControl() {
				out = append(out, r.record(kr))
			}
		}
	}
	slices.SortFunc(out, func(a, b Record) int {
		return cmp.Or(cmp.Compare(a.Partition, b.Partition), cmp.Compare(a.Offset, b.Offset))
	})
	return out, nil
}

// fetchErr is the first fetch error that is not the poll ending.
func fetchErr(fs kgo.Fetches, topic string) error {
	for _, fe := range fs.Errors() {
		if errors.Is(fe.Err, context.Canceled) || errors.Is(fe.Err, context.DeadlineExceeded) {
			continue
		}
		if errors.Is(fe.Err, kerr.OffsetOutOfRange) {
			return fmt.Errorf("%s p%d: %w", topic, fe.Partition, fe.Err)
		}
		return fe.Err
	}
	return nil
}

func partitionBounds(ctx context.Context, adm *kadm.Client, topic string) (starts, ends map[int32]int64, err error) {
	s, err := adm.ListStartOffsets(ctx, topic)
	if err != nil {
		return nil, nil, err
	}
	e, err := adm.ListEndOffsets(ctx, topic)
	if err != nil {
		return nil, nil, err
	}
	starts, ends = map[int32]int64{}, map[int32]int64{}
	s.Each(func(o kadm.ListedOffset) {
		if o.Err == nil {
			starts[o.Partition] = o.Offset
		}
	})
	e.Each(func(o kadm.ListedOffset) {
		if o.Err == nil {
			ends[o.Partition] = o.Offset
		}
	})
	if len(ends) == 0 {
		return nil, nil, fmt.Errorf("topic %s doesn't exist", topic)
	}
	return starts, ends, nil
}

func (r *reader) Tail(_ context.Context, topic string, from map[int32]int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopTail()
	kcl, err := r.consumer(topic, from)
	if err != nil {
		return err
	}
	r.tail = kcl
	return nil
}

func (r *reader) Poll(ctx context.Context, maxRecs int) ([]Record, error) {
	r.mu.Lock()
	kcl := r.tail
	r.mu.Unlock()
	if kcl == nil {
		return nil, errors.New("kafka: not tailing")
	}
	fs := kcl.PollRecords(ctx, maxRecs)
	if fs.IsClientClosed() {
		return nil, nil
	}
	var topic string
	if recs := fs.Records(); len(recs) > 0 {
		topic = recs[0].Topic
	}
	if err := fetchErr(fs, topic); err != nil {
		return nil, wrap(context.Background(), err, "topic "+topic)
	}
	var out []Record
	for _, kr := range fs.Records() {
		if !kr.Attrs.IsControl() {
			out = append(out, r.record(kr))
		}
	}
	return out, nil
}

func (r *reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopTail()
	return nil
}

// record copies a kgo record, cutting the value to MaxValueBytes. The
// bytes are copied: kgo's point into the whole fetched batch, which would
// stay in memory as long as any of them.
func (r *reader) record(kr *kgo.Record) Record {
	rec := Record{
		Topic: kr.Topic, Partition: kr.Partition, Offset: kr.Offset, Time: kr.Timestamp,
		TimeType: "CreateTime",
		Key:      clone(kr.Key), Value: kr.Value, KeyNull: kr.Key == nil, ValueNull: kr.Value == nil,
		Size: len(kr.Key) + len(kr.Value),
	}
	if kr.Attrs.TimestampType() == 1 {
		rec.TimeType = "LogAppendTime"
	}
	for _, h := range kr.Headers {
		rec.Headers = append(rec.Headers, Header{Key: h.Key, Value: clone(h.Value)})
		rec.Size += len(h.Key) + len(h.Value)
	}
	if m := r.rc.MaxValueBytes; m > 0 && len(rec.Value) > m {
		rec.Value = rec.Value[:m]
		rec.Truncated = true
	}
	rec.Value = clone(rec.Value)
	return rec
}

// clone copies b, keeping nil nil and empty empty.
func clone(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append(make([]byte, 0, len(b)), b...)
}
