package kafka

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
)

// ConfigChange sets one topic config to Value, or (Delete) removes its
// override. Old and OldDefault are what the preview showed.
type ConfigChange struct {
	Name, Value string
	Delete      bool
	Old         string
	OldDefault  bool
}

// ErrConfigChanged is a config change whose configs moved since the preview.
var ErrConfigChanged = errors.New("topic configs changed since preview; review again")

// AlterTopicConfigs re-reads the configs, gives up with ErrConfigChanged
// when one of changes is no longer at Old, and sends only changes, as
// incremental SetConfig / DeleteConfig: configs not in changes keep their
// values.
func (c *client) AlterTopicConfigs(ctx context.Context, topic string, changes []ConfigChange) error {
	current, err := c.TopicConfigs(ctx, topic)
	if err != nil {
		return err
	}
	byName := make(map[string]ConfigEntry, len(current))
	for _, e := range current {
		byName[e.Name] = e
	}
	alters := make([]kadm.AlterConfig, 0, len(changes))
	for _, ch := range changes {
		e, ok := byName[ch.Name]
		if !ok || e.Value != ch.Old || e.Default != ch.OldDefault {
			return ErrConfigChanged
		}
		if ch.Delete {
			alters = append(alters, kadm.AlterConfig{Op: kadm.DeleteConfig, Name: ch.Name})
			continue
		}
		v := ch.Value
		alters = append(alters, kadm.AlterConfig{Op: kadm.SetConfig, Name: ch.Name, Value: &v})
	}
	if len(alters) == 0 {
		return nil
	}
	what := "topic " + topic
	resp, err := c.adm.AlterTopicConfigs(ctx, alters, topic)
	if err != nil {
		return wrap(ctx, err, what)
	}
	r, err := resp.On(topic, nil)
	if err == nil {
		err = r.Err
		if err != nil && r.ErrMessage != "" {
			err = fmt.Errorf("%w: %s", err, r.ErrMessage)
		}
	}
	return wrap(ctx, err, what)
}

// AddPartitions grows topic to total partitions; fewer than now is an error.
func (c *client) AddPartitions(ctx context.Context, topic string, total int) error {
	d, err := c.TopicDetail(ctx, topic)
	if err != nil {
		return err
	}
	if total <= len(d.Parts) {
		return fmt.Errorf("a topic's partitions can only grow (%s has %d)", topic, len(d.Parts))
	}
	what := "topic " + topic
	resp, err := c.adm.UpdatePartitions(ctx, total, topic)
	if err != nil {
		return wrap(ctx, err, what)
	}
	r, ok := resp[topic]
	if !ok {
		return fmt.Errorf("topic %s: no answer", topic)
	}
	if r.Err != nil && r.ErrMessage != "" {
		return wrap(ctx, fmt.Errorf("%w: %s", r.Err, r.ErrMessage), what)
	}
	return wrap(ctx, r.Err, what)
}

// DeleteResult is one partition of a DeleteRecords.
type DeleteResult struct {
	Partition int32
	Start     int64 // the partition's start offset afterwards
	Err       error
}

// ErrGroupSubscribed is a delete of offsets on a topic the group still consumes.
var ErrGroupSubscribed = errors.New("the group still consumes this topic — stop the consumers first")

// DeleteRecords deletes each partition's messages before before[p];
// offsets outside [start, end] are an error before any request.
func (c *client) DeleteRecords(ctx context.Context, topic string, before map[int32]int64) ([]DeleteResult, error) {
	if !c.Supports("delete_records") {
		return nil, ErrUnsupported
	}
	what := "topic " + topic
	starts, ends, err := partitionBounds(ctx, c.adm, topic, false)
	if err != nil {
		return nil, wrap(ctx, err, what)
	}
	var os kadm.Offsets
	parts := make([]int32, 0, len(before))
	for p, o := range before {
		end, ok := ends[p]
		if !ok {
			return nil, fmt.Errorf("partition %d of %s doesn't exist", p, topic)
		}
		if o < starts[p] || o > end {
			return nil, ErrOutOfRange{Partition: p, Start: starts[p], End: end}
		}
		os.Add(kadm.Offset{Topic: topic, Partition: p, At: o, LeaderEpoch: -1})
		parts = append(parts, p)
	}
	slices.Sort(parts)
	resp, err := c.adm.DeleteRecords(ctx, os)
	if err != nil {
		return nil, wrap(ctx, err, what)
	}
	out := make([]DeleteResult, len(parts))
	for i, p := range parts {
		out[i] = DeleteResult{Partition: p}
		r, ok := resp.Lookup(topic, p)
		switch {
		case !ok:
			out[i].Err = errors.New("no answer for this partition")
		case r.Err != nil:
			out[i].Err = wrap(ctx, r.Err, what)
		default:
			out[i].Start = r.LowWatermark
		}
	}
	return out, nil
}

// deleteRefusal is why a group of this type cannot be deleted here.
func deleteRefusal(t GroupType) error {
	if t.HasLag() {
		return nil
	}
	return fmt.Errorf("can't delete a %s group here", t)
}

// DeleteGroup deletes an Empty group (ErrGroupNotEmpty otherwise; the
// state is read again here). Share and unknown groups are refused.
func (c *client) DeleteGroup(ctx context.Context, group string) error {
	if err := c.groupEmpty(ctx, group, deleteRefusal); err != nil {
		return err
	}
	_, err := c.adm.DeleteGroup(ctx, group)
	if errors.Is(err, kerr.NonEmptyGroup) {
		return ErrGroupNotEmpty
	}
	return wrap(ctx, err, "group "+group)
}

// DeleteGroupOffsets deletes the group's committed offsets on topic; the
// group must be Empty or no longer subscribe to topic (the broker checks:
// ErrGroupSubscribed).
func (c *client) DeleteGroupOffsets(ctx context.Context, group, topic string) ([]CommitResult, error) {
	what := "group " + group
	g, err := c.findGroup(ctx, group)
	if err != nil {
		return nil, err
	}
	if err := deleteRefusal(g.Type); err != nil {
		return nil, err
	}
	fetched, err := c.adm.FetchOffsets(ctx, group)
	if err != nil {
		return nil, wrap(ctx, err, what)
	}
	var tps []TP
	for tp := range offsetsOf(fetched) {
		if tp.Topic == topic {
			tps = append(tps, tp)
		}
	}
	if len(tps) == 0 {
		return nil, fmt.Errorf("%s has no offsets on %s", group, topic)
	}
	slices.SortFunc(tps, func(a, b TP) int { return int(a.Partition - b.Partition) })
	set := kadm.TopicsSet{}
	for _, tp := range tps {
		set.Add(topic, tp.Partition)
	}
	resp, err := c.adm.DeleteOffsets(ctx, group, set)
	if errors.Is(err, kerr.GroupSubscribedToTopic) {
		return nil, ErrGroupSubscribed
	}
	if err != nil {
		return nil, wrap(ctx, err, what)
	}
	out := make([]CommitResult, len(tps))
	for i, tp := range tps {
		out[i] = CommitResult{TP: tp, Offset: -1}
		perr, ok := resp.Lookup(topic, tp.Partition)
		switch {
		case !ok:
			out[i].Err = errors.New("no answer for this partition")
		case errors.Is(perr, kerr.GroupSubscribedToTopic):
			// brokers check the subscription per topic: no partition was deleted
			return nil, ErrGroupSubscribed
		case perr != nil:
			out[i].Err = wrap(ctx, perr, what)
		}
	}
	return out, nil
}
