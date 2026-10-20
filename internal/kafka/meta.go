package kafka

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// Broker is one broker of the cluster.
type Broker struct {
	ID         int32
	Host       string
	Port       int32
	Rack       string
	Controller bool
	Leaders    int // partitions this broker leads
}

// ClusterInfo describes the cluster.
type ClusterInfo struct {
	ID         string
	Controller int32
	Brokers    []Broker // by ID
	Version    string   // guessed from ApiVersions: "3.7", "≥ 4.0"
}

// Topic is one topic of the topic list.
type Topic struct {
	Name                     string
	Partitions, Replication  int
	Internal                 bool
	UnderReplicated, Offline int // partitions with ISR < replicas, without a leader
}

// Partition is one partition of a topic, with its offsets.
type Partition struct {
	ID, Leader             int32 // Leader -1: offline
	Replicas, ISR, Offline []int32
	Start, End             int64
}

// TopicDetail is a topic with its partitions, by ID.
type TopicDetail struct {
	Topic
	Parts []Partition
}

// ConfigEntry is one topic config.
type ConfigEntry struct {
	Name, Value, Source string // Source: default, topic, broker, …
	Default, Sensitive  bool
}

func (c *client) Cluster(ctx context.Context) (ClusterInfo, error) {
	m, err := c.adm.Metadata(ctx)
	if err != nil {
		return ClusterInfo{}, wrap(ctx, err, "cluster")
	}
	leaders := map[int32]int{}
	for _, t := range m.Topics {
		for _, p := range t.Partitions {
			leaders[p.Leader]++
		}
	}
	info := ClusterInfo{ID: m.Cluster, Controller: m.Controller, Version: c.versionGuess()}
	for _, b := range m.Brokers {
		br := Broker{ID: b.NodeID, Host: b.Host, Port: b.Port, Controller: b.NodeID == m.Controller, Leaders: leaders[b.NodeID]}
		if b.Rack != nil {
			br.Rack = *b.Rack
		}
		info.Brokers = append(info.Brokers, br)
	}
	slices.SortFunc(info.Brokers, func(a, b Broker) int { return cmp.Compare(a.ID, b.ID) })
	return info, nil
}

// versionGuess is kversion's guess for the first broker that answered,
// as "3.7" or "≥ 4.0".
func (c *client) versionGuess() string {
	for _, v := range c.versions.Sorted() {
		if v.Err != nil {
			continue
		}
		g := v.VersionGuess()
		if rest, ok := strings.CutPrefix(g, "at least "); ok {
			return "≥ " + strings.TrimPrefix(rest, "v")
		}
		return strings.TrimPrefix(g, "v")
	}
	return ""
}

func (c *client) Topics(ctx context.Context) ([]Topic, error) {
	m, err := c.adm.Metadata(ctx)
	if err != nil {
		return nil, wrap(ctx, err, "topics")
	}
	out := make([]Topic, 0, len(m.Topics))
	for _, t := range m.Topics {
		if t.Err != nil {
			continue
		}
		tp := Topic{Name: t.Topic, Partitions: len(t.Partitions), Internal: t.IsInternal}
		for _, p := range t.Partitions {
			tp.Replication = max(tp.Replication, len(p.Replicas))
			if p.Leader < 0 {
				tp.Offline++
			} else if len(p.ISR) < len(p.Replicas) {
				tp.UnderReplicated++
			}
		}
		out = append(out, tp)
	}
	slices.SortFunc(out, func(a, b Topic) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (c *client) TopicDetail(ctx context.Context, topic string) (TopicDetail, error) {
	what := "topic " + topic
	m, err := c.adm.Metadata(ctx, topic)
	if err != nil {
		return TopicDetail{}, wrap(ctx, err, what)
	}
	t, ok := m.Topics[topic]
	if !ok || errors.Is(t.Err, kerr.UnknownTopicOrPartition) {
		return TopicDetail{}, fmt.Errorf("topic %s doesn't exist", topic)
	}
	if t.Err != nil {
		return TopicDetail{}, wrap(ctx, t.Err, what)
	}
	starts, err := c.adm.ListStartOffsets(ctx, topic)
	if err != nil {
		return TopicDetail{}, wrap(ctx, err, what)
	}
	ends, err := c.adm.ListEndOffsets(ctx, topic)
	if err != nil {
		return TopicDetail{}, wrap(ctx, err, what)
	}
	d := TopicDetail{Topic: Topic{Name: topic, Partitions: len(t.Partitions), Internal: t.IsInternal}}
	for _, p := range t.Partitions.Sorted() {
		part := Partition{ID: p.Partition, Leader: p.Leader, Replicas: p.Replicas, ISR: p.ISR, Offline: p.OfflineReplicas, Start: -1, End: -1}
		if o, ok := starts.Lookup(topic, p.Partition); ok && o.Err == nil {
			part.Start = o.Offset
		}
		if o, ok := ends.Lookup(topic, p.Partition); ok && o.Err == nil {
			part.End = o.Offset
		}
		d.Replication = max(d.Replication, len(p.Replicas))
		if p.Leader < 0 {
			d.Offline++
		} else if len(p.ISR) < len(p.Replicas) {
			d.UnderReplicated++
		}
		d.Parts = append(d.Parts, part)
	}
	return d, nil
}

func (c *client) TopicConfigs(ctx context.Context, topic string) ([]ConfigEntry, error) {
	what := "topic " + topic
	rcs, err := c.adm.DescribeTopicConfigs(ctx, topic)
	if err != nil {
		return nil, wrap(ctx, err, what)
	}
	rc, err := rcs.On(topic, nil)
	if err == nil {
		err = rc.Err
	}
	if errors.Is(err, kerr.UnknownTopicOrPartition) {
		return nil, fmt.Errorf("topic %s doesn't exist", topic)
	}
	if err != nil {
		return nil, wrap(ctx, err, what)
	}
	out := make([]ConfigEntry, 0, len(rc.Configs))
	for _, cf := range rc.Configs {
		e := ConfigEntry{Name: cf.Key, Sensitive: cf.Sensitive, Source: configSource(cf.Source),
			Default: cf.Source == kmsg.ConfigSourceDefaultConfig}
		if cf.Value != nil && !cf.Sensitive {
			e.Value = *cf.Value
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b ConfigEntry) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func configSource(s kmsg.ConfigSource) string {
	switch s {
	case kmsg.ConfigSourceDynamicTopicConfig:
		return "topic"
	case kmsg.ConfigSourceDynamicBrokerConfig:
		return "broker"
	case kmsg.ConfigSourceDynamicDefaultBrokerConfig:
		return "cluster"
	case kmsg.ConfigSourceStaticBrokerConfig:
		return "static broker"
	case kmsg.ConfigSourceDynamicBrokerLoggerConfig:
		return "broker logger"
	case kmsg.ConfigSourceGroupConfig:
		return "group"
	}
	return "default"
}
