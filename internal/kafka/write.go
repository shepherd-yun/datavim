package kafka

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// ProduceRecord is one message to send; Partition -1 lets the key decide.
type ProduceRecord struct {
	Key, Value         []byte
	KeyNull, ValueNull bool
	Partition          int32
	Headers            []Header
}

// ProduceResult is where one message landed, or why it did not.
type ProduceResult struct {
	Partition int32
	Offset    int64
	Time      time.Time
	Err       error
}

// NewTopic is a topic to create; Replication -1 is the broker default.
type NewTopic struct {
	Name        string
	Partitions  int32
	Replication int16
	Configs     map[string]string
}

// OffsetCommit moves one partition's committed offset to To. Expect is
// the committed offset the preview showed (-1: none).
type OffsetCommit struct {
	TP
	Expect, To int64
}

// CommitResult is the outcome of one OffsetCommit.
type CommitResult struct {
	TP
	Offset int64
	Err    error
}

var (
	// ErrGroupNotEmpty is a reset of a group with members: Kafka refuses
	// their commits, and live members would overwrite them anyway.
	ErrGroupNotEmpty = errors.New("group has active members — stop the consumers first")
	// ErrGroupChanged is a reset whose group moved since the preview.
	ErrGroupChanged = errors.New("group changed since preview; review again")
)

// partitionKey marks a record whose partition the user chose.
type partitionKey struct{}

// partitioner sends records with a chosen partition there and hashes the
// key of the others as Kafka's Java client does.
type partitioner struct{ keyed kgo.Partitioner }

func newPartitioner() kgo.Partitioner {
	return partitioner{kgo.StickyKeyPartitioner(nil)}
}

func (p partitioner) ForTopic(t string) kgo.TopicPartitioner {
	return topicPartitioner{p.keyed.ForTopic(t)}
}

type topicPartitioner struct{ keyed kgo.TopicPartitioner }

func (p topicPartitioner) RequiresConsistency(*kgo.Record) bool { return true }
func (p topicPartitioner) Partition(r *kgo.Record, n int) int {
	if r.Context != nil {
		if v, ok := r.Context.Value(partitionKey{}).(int32); ok {
			return int(v)
		}
	}
	return p.keyed.Partition(r, n)
}

// Produce sends recs to topic and waits for every acknowledgement. The
// topic is looked up first: a missing one is an error and nothing is sent
// (and never created).
func (c *client) Produce(ctx context.Context, topic string, recs []ProduceRecord) ([]ProduceResult, error) {
	d, err := c.TopicDetail(ctx, topic)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if r.Partition >= int32(len(d.Parts)) {
			return nil, fmt.Errorf("partition %d doesn't exist (%s has %d)", r.Partition, topic, len(d.Parts))
		}
	}
	kcl, err := c.producer()
	if err != nil {
		return nil, err
	}
	out := produceSync(ctx, kcl, topic, recs)
	if kcl == c.kcl && allFailed(out, kerr.ClusterAuthorizationFailed) {
		// before Kafka 3.0 an idempotent producer needs the IDEMPOTENT_WRITE
		// cluster ACL; nothing was written, so send again without it
		c.mu.Lock()
		c.noIdempotent = true
		c.mu.Unlock()
		if kcl, err = c.producer(); err != nil {
			return nil, err
		}
		out = produceSync(ctx, kcl, topic, recs)
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, nil
}

// producer is the client Produce sends with: the shared one, or one
// without idempotent writes once the cluster refused them.
func (c *client) producer() (*kgo.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.noIdempotent {
		return c.kcl, nil
	}
	if c.plain == nil {
		kcl, err := kgo.NewClient(append(slices.Clone(c.opts), kgo.RecordPartitioner(newPartitioner()), kgo.DisableIdempotentWrite())...)
		if err != nil {
			return nil, err
		}
		c.plain = kcl
	}
	return c.plain, nil
}

func produceSync(ctx context.Context, kcl *kgo.Client, topic string, recs []ProduceRecord) []ProduceResult {
	krs := make([]*kgo.Record, len(recs))
	for i, r := range recs {
		kr := &kgo.Record{Topic: topic, Key: r.Key, Value: r.Value, Context: ctx}
		if r.KeyNull {
			kr.Key = nil
		} else if kr.Key == nil {
			kr.Key = []byte{}
		}
		if r.ValueNull {
			kr.Value = nil
		} else if kr.Value == nil {
			kr.Value = []byte{}
		}
		if r.Partition >= 0 {
			kr.Context = context.WithValue(ctx, partitionKey{}, r.Partition)
		}
		for _, h := range r.Headers {
			kr.Headers = append(kr.Headers, kgo.RecordHeader{Key: h.Key, Value: h.Value})
		}
		krs[i] = kr
	}
	out := make([]ProduceResult, len(krs))
	for i, pr := range kcl.ProduceSync(ctx, krs...) {
		out[i] = ProduceResult{
			Partition: pr.Record.Partition, Offset: pr.Record.Offset, Time: pr.Record.Timestamp,
			Err: wrap(ctx, pr.Err, "topic "+topic),
		}
		if pr.Err != nil {
			out[i].Partition, out[i].Offset = -1, -1
		}
	}
	return out
}

// allFailed reports whether every result failed with err.
func allFailed(out []ProduceResult, err error) bool {
	for _, r := range out {
		if !errors.Is(r.Err, err) {
			return false
		}
	}
	return len(out) > 0
}

func (c *client) CreateTopic(ctx context.Context, t NewTopic) error {
	var cfgs map[string]*string
	for k, v := range t.Configs {
		if cfgs == nil {
			cfgs = map[string]*string{}
		}
		cfgs[k] = &v
	}
	resp, err := c.adm.CreateTopic(ctx, t.Partitions, t.Replication, cfgs, t.Name)
	if err == nil {
		err = resp.Err
	}
	if errors.Is(err, kerr.TopicAlreadyExists) {
		return fmt.Errorf("topic %s already exists", t.Name)
	}
	return wrap(ctx, err, "topic "+t.Name)
}

func (c *client) DeleteTopic(ctx context.Context, name string) error {
	resp, err := c.adm.DeleteTopic(ctx, name)
	if err == nil {
		err = resp.Err
	}
	if errors.Is(err, kerr.UnknownTopicOrPartition) {
		return fmt.Errorf("topic %s doesn't exist", name)
	}
	return wrap(ctx, err, "topic "+name)
}

// resetRefusal is why offsets of a group of this type cannot be reset.
func resetRefusal(t GroupType) error {
	if t.HasLag() {
		return nil
	}
	return fmt.Errorf("can't reset offsets of a %s group", t)
}

// groupEmpty reads group's state again, here and not from the UI's old
// copy: refusal's error for a group type that cannot be changed, and
// ErrGroupNotEmpty unless the group is Empty (or Dead).
func (c *client) groupEmpty(ctx context.Context, group string, refusal func(GroupType) error) error {
	what := "group " + group
	g, err := c.findGroup(ctx, group)
	if err != nil {
		return err
	}
	if err := refusal(g.Type); err != nil {
		return err
	}
	state := g.State
	switch g.Type {
	case GroupClassic:
		ds, err := c.adm.DescribeGroups(ctx, group)
		if err != nil {
			return wrap(ctx, err, what)
		}
		dg := ds[group]
		if dg.Err != nil {
			return wrap(ctx, dg.Err, what)
		}
		state = normalizeState(dg.State)
		if len(dg.Members) > 0 {
			state = "Stable"
		}
	case GroupConsumer:
		s, _, members, err := c.describeConsumerGroup(ctx, group)
		if err != nil {
			return wrap(ctx, err, what)
		}
		if state = s; len(members) > 0 {
			state = "Stable"
		}
	}
	if state != "Empty" && state != "Dead" {
		return ErrGroupNotEmpty
	}
	return nil
}

// ResetOffsets commits commits for group, only when the group is Empty and
// every partition's committed offset is still what the preview showed.
// Only the partitions in commits are touched.
func (c *client) ResetOffsets(ctx context.Context, group string, commits []OffsetCommit) ([]CommitResult, error) {
	what := "group " + group
	if err := c.groupEmpty(ctx, group, resetRefusal); err != nil {
		return nil, err
	}
	fetched, err := c.adm.FetchOffsets(ctx, group)
	if err != nil {
		return nil, wrap(ctx, err, what)
	}
	current := offsetsOf(fetched)
	var os kadm.Offsets
	for _, cm := range commits {
		now, ok := current[cm.TP]
		if !ok {
			now = -1
		}
		if now != cm.Expect {
			return nil, ErrGroupChanged
		}
		os.Add(kadm.Offset{Topic: cm.Topic, Partition: cm.Partition, At: cm.To, LeaderEpoch: -1})
	}
	resp, err := c.adm.CommitOffsets(ctx, group, os)
	if err != nil {
		return nil, wrap(ctx, err, what)
	}
	out := make([]CommitResult, len(commits))
	for i, cm := range commits {
		out[i] = CommitResult{TP: cm.TP, Offset: cm.To}
		if r, ok := resp.Lookup(cm.Topic, cm.Partition); !ok {
			out[i].Err = errors.New("no answer for this partition")
		} else if r.Err != nil {
			out[i].Err = wrap(ctx, r.Err, what)
		}
	}
	return out, nil
}
