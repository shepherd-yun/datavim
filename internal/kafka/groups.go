package kafka

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// GroupType is the protocol a group runs.
type GroupType string

const (
	GroupClassic  GroupType = "classic"
	GroupConsumer GroupType = "consumer" // KIP-848
	GroupShare    GroupType = "share"    // KIP-932
	GroupConnect  GroupType = "connect"
	GroupUnknown  GroupType = "unknown"
)

// Group is one consumer group.
type Group struct {
	Name     string
	State    string // Stable, Empty, Rebalancing, Dead, or the broker's word
	Protocol string // the assignor (range, …); set by GroupDetail
	Type     GroupType
	Members  int // set by GroupDetail
}

// Member is one member of a group.
type Member struct {
	ID, ClientID, Host string
	Assigned           []TP
}

// GroupDetail is a group with its members and its progress on every
// partition of the topics it commits to or is assigned, by lag, largest
// first.
type GroupDetail struct {
	Group
	Members []Member
	Lag     []PartitionLag
}

// HasLag reports whether the type has offsets to compute lag from.
func (t GroupType) HasLag() bool { return t == GroupClassic || t == GroupConsumer }

// Groups lists every group, sorted by name.
func (c *client) Groups(ctx context.Context) ([]Group, error) {
	req := kmsg.NewPtrListGroupsRequest()
	var out []Group
	for _, shard := range c.kcl.RequestSharded(ctx, req) {
		if shard.Err != nil {
			return nil, wrap(ctx, shard.Err, "groups")
		}
		resp := shard.Resp.(*kmsg.ListGroupsResponse)
		if err := kerr.ErrorForCode(resp.ErrorCode); err != nil {
			return nil, wrap(ctx, err, "groups")
		}
		for _, g := range resp.Groups {
			out = append(out, Group{Name: g.Group, State: normalizeState(g.GroupState), Type: groupType(g.GroupType, g.ProtocolType)})
		}
	}
	slices.SortFunc(out, func(a, b Group) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// groupType reads ListGroups' GroupType (Kafka 3.8+), else guesses from
// the protocol type: offsets-only groups have none and are classic.
func groupType(typ, protocolType string) GroupType {
	if protocolType == "connect" {
		return GroupConnect
	}
	switch strings.ToLower(typ) {
	case "classic":
		return GroupClassic
	case "consumer":
		return GroupConsumer
	case "share":
		return GroupShare
	case "":
		if protocolType == "" || protocolType == "consumer" {
			return GroupClassic
		}
	}
	return GroupUnknown
}

// normalizeState folds the rebalancing states of both protocols into one.
func normalizeState(s string) string {
	switch strings.ToLower(s) {
	case "stable":
		return "Stable"
	case "empty":
		return "Empty"
	case "dead":
		return "Dead"
	case "preparingrebalance", "completingrebalance", "assigning", "reconciling":
		return "Rebalancing"
	}
	return s
}

func (c *client) findGroup(ctx context.Context, group string) (Group, error) {
	groups, err := c.Groups(ctx)
	if err != nil {
		return Group{}, err
	}
	for _, g := range groups {
		if g.Name == group {
			return g, nil
		}
	}
	return Group{}, fmt.Errorf("group %s doesn't exist", group)
}

func (c *client) GroupDetail(ctx context.Context, group string) (GroupDetail, error) {
	what := "group " + group
	g, err := c.findGroup(ctx, group)
	if err != nil {
		return GroupDetail{}, err
	}
	d := GroupDetail{Group: g}
	switch g.Type {
	case GroupClassic, GroupConnect:
		ds, err := c.adm.DescribeGroups(ctx, group)
		if err != nil {
			return GroupDetail{}, wrap(ctx, err, what)
		}
		dg := ds[group]
		if dg.Err != nil {
			return GroupDetail{}, wrap(ctx, dg.Err, what)
		}
		d.State, d.Protocol = normalizeState(dg.State), dg.Protocol
		for _, m := range dg.Members {
			mem := Member{ID: m.MemberID, ClientID: m.ClientID, Host: m.ClientHost}
			if a, ok := m.Assigned.AsConsumer(); ok {
				for _, t := range a.Topics {
					for _, p := range t.Partitions {
						mem.Assigned = append(mem.Assigned, TP{Topic: t.Topic, Partition: p})
					}
				}
			}
			d.Members = append(d.Members, mem)
		}
	case GroupConsumer:
		state, assignor, members, err := c.describeConsumerGroup(ctx, group)
		if err != nil {
			return GroupDetail{}, wrap(ctx, err, what)
		}
		d.State, d.Protocol, d.Members = state, assignor, members
	default:
		return d, nil // share / unknown: no members or lag here
	}
	for i := range d.Members {
		slices.SortFunc(d.Members[i].Assigned, compareTP)
	}
	d.Group.Members = len(d.Members)
	if !g.Type.HasLag() {
		return d, nil
	}
	committed, err := c.adm.FetchOffsets(ctx, group)
	if err != nil {
		return GroupDetail{}, wrap(ctx, err, what)
	}
	topics := map[string]bool{}
	committed.Each(func(o kadm.OffsetResponse) { topics[o.Topic] = true })
	owner := map[TP]string{}
	for _, m := range d.Members {
		for _, tp := range m.Assigned {
			topics[tp.Topic] = true
			owner[tp] = m.ClientID
		}
	}
	lags, err := c.lags(ctx, slices.Sorted(maps.Keys(topics)), offsetsOf(committed), what)
	if err != nil {
		return GroupDetail{}, err
	}
	for i := range lags {
		lags[i].Member = owner[lags[i].TP]
	}
	d.Lag = lags
	return d, nil
}

// describeConsumerGroup sends ConsumerGroupDescribe (KIP-848) itself:
// brokers may name assigned topics by ID only, which kadm leaves unnamed.
func (c *client) describeConsumerGroup(ctx context.Context, group string) (state, assignor string, members []Member, err error) {
	req := kmsg.NewPtrConsumerGroupDescribeRequest()
	req.Groups = []string{group}
	var names map[[16]byte]string
	for _, shard := range c.kcl.RequestSharded(ctx, req) {
		if shard.Err != nil {
			return "", "", nil, shard.Err
		}
		for _, g := range shard.Resp.(*kmsg.ConsumerGroupDescribeResponse).Groups {
			if g.Group != group {
				continue
			}
			if err := kerr.ErrorForCode(g.ErrorCode); err != nil {
				return "", "", nil, err
			}
			for _, m := range g.Members {
				mem := Member{ID: m.MemberID, ClientID: m.ClientID, Host: m.ClientHost}
				for _, tp := range m.Assignment.TopicPartitions {
					topic := tp.Topic
					if topic == "" {
						if names == nil {
							if names, err = c.topicNames(ctx); err != nil {
								return "", "", nil, err
							}
						}
						topic = names[tp.TopicID]
					}
					for _, p := range tp.Partitions {
						mem.Assigned = append(mem.Assigned, TP{Topic: topic, Partition: p})
					}
				}
				members = append(members, mem)
			}
			slices.SortFunc(members, func(a, b Member) int { return strings.Compare(a.ID, b.ID) })
			return normalizeState(g.State), g.AssignorName, members, nil
		}
	}
	return "", "", nil, fmt.Errorf("group %s doesn't exist", group)
}

// topicNames maps topic IDs to names.
func (c *client) topicNames(ctx context.Context) (map[[16]byte]string, error) {
	m, err := c.adm.Metadata(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[[16]byte]string, len(m.Topics))
	for name, t := range m.Topics {
		out[t.ID] = name
	}
	return out, nil
}

func compareTP(a, b TP) int {
	return cmp.Or(strings.Compare(a.Topic, b.Topic), cmp.Compare(a.Partition, b.Partition))
}

// offsetsOf keeps the committed offsets that fetched without error.
func offsetsOf(rs kadm.OffsetResponses) map[TP]int64 {
	out := map[TP]int64{}
	rs.Each(func(o kadm.OffsetResponse) {
		if o.Err == nil && o.At >= 0 {
			out[TP{o.Topic, o.Partition}] = o.At
		}
	})
	return out
}

// lags computes the progress of committed on every partition of topics,
// sorted by lag (largest first), then topic and partition.
func (c *client) lags(ctx context.Context, topics []string, committed map[TP]int64, what string) ([]PartitionLag, error) {
	if len(topics) == 0 {
		return nil, nil
	}
	starts, ends, err := c.bounds(ctx, topics, what)
	if err != nil {
		return nil, err
	}
	var out []PartitionLag
	for tp, end := range ends {
		l := PartitionLag{TP: tp, Committed: -1, Start: starts[tp], End: end}
		if o, ok := committed[tp]; ok {
			l.Committed = o
			l.Lag = max(0, end-o)
		} else {
			l.Lag = max(0, end-l.Start)
		}
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b PartitionLag) int { return cmp.Or(cmp.Compare(b.Lag, a.Lag), compareTP(a.TP, b.TP)) })
	return out, nil
}

// bounds lists the start and end offsets of every partition of topics;
// partitions that fail to list are left out.
func (c *client) bounds(ctx context.Context, topics []string, what string) (starts, ends map[TP]int64, err error) {
	s, err := c.adm.ListStartOffsets(ctx, topics...)
	if err != nil {
		return nil, nil, wrap(ctx, err, what)
	}
	e, err := c.adm.ListEndOffsets(ctx, topics...)
	if err != nil {
		return nil, nil, wrap(ctx, err, what)
	}
	starts, ends = map[TP]int64{}, map[TP]int64{}
	s.Each(func(o kadm.ListedOffset) {
		if o.Err == nil {
			starts[TP{o.Topic, o.Partition}] = o.Offset
		}
	})
	e.Each(func(o kadm.ListedOffset) {
		if o.Err == nil {
			ends[TP{o.Topic, o.Partition}] = o.Offset
		}
	})
	return starts, ends, nil
}

// GroupsLag sums each group's lag over every partition of the topics it
// committed to, in one round of requests. Groups whose offsets cannot be
// fetched (share groups, missing ones) are left out.
func (c *client) GroupsLag(ctx context.Context, groups []string) (map[string]int64, error) {
	if len(groups) == 0 {
		return map[string]int64{}, nil
	}
	fetched := c.adm.FetchManyOffsets(ctx, groups...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	topics := map[string]bool{}
	committed := map[string]map[TP]int64{}
	for g, r := range fetched {
		if r.Err != nil {
			continue
		}
		committed[g] = offsetsOf(r.Fetched)
		r.Fetched.Each(func(o kadm.OffsetResponse) { topics[o.Topic] = true })
	}
	out := make(map[string]int64, len(committed))
	if len(topics) == 0 {
		for g := range committed {
			out[g] = 0
		}
		return out, nil
	}
	starts, ends, err := c.bounds(ctx, slices.Sorted(maps.Keys(topics)), "groups")
	if err != nil {
		return nil, err
	}
	for g, offs := range committed {
		mine := map[string]bool{}
		for tp := range offs {
			mine[tp.Topic] = true
		}
		var sum int64
		for tp, end := range ends {
			if !mine[tp.Topic] {
				continue
			}
			if o, ok := offs[tp]; ok {
				sum += max(0, end-o)
			} else {
				sum += max(0, end-starts[tp])
			}
		}
		out[g] = sum
	}
	return out, nil
}

// TopicGroups lists the groups with committed offsets on topic; their Lag
// covers only topic and their members are not described.
func (c *client) TopicGroups(ctx context.Context, topic string) ([]GroupDetail, error) {
	groups, err := c.Groups(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	byName := map[string]Group{}
	for _, g := range groups {
		if g.Type.HasLag() {
			names = append(names, g.Name)
			byName[g.Name] = g
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	fetched := c.adm.FetchManyOffsets(ctx, names...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var out []GroupDetail
	for _, name := range names {
		r, ok := fetched[name]
		if !ok || r.Err != nil {
			continue
		}
		offs := map[TP]int64{}
		for tp, o := range offsetsOf(r.Fetched) {
			if tp.Topic == topic {
				offs[tp] = o
			}
		}
		if len(offs) == 0 {
			continue
		}
		lags, err := c.lags(ctx, []string{topic}, offs, "topic "+topic)
		if err != nil {
			return nil, err
		}
		out = append(out, GroupDetail{Group: byName[name], Lag: lags})
	}
	return out, nil
}

// OffsetsAt is, for each partition of topic, the first offset whose
// timestamp is at or after ts, or the end offset when there is none.
func (c *client) OffsetsAt(ctx context.Context, topic string, ts time.Time) (map[int32]int64, error) {
	what := "topic " + topic
	if !c.Supports("offsets_by_time") {
		return nil, ErrUnsupported
	}
	at, err := c.adm.ListOffsetsAfterMilli(ctx, ts.UnixMilli(), topic)
	if err != nil {
		return nil, wrap(ctx, err, what)
	}
	_, ends, err := c.bounds(ctx, []string{topic}, what)
	if err != nil {
		return nil, err
	}
	out := map[int32]int64{}
	for tp, end := range ends {
		out[tp.Partition] = end
		if o, ok := at.Lookup(topic, tp.Partition); ok && o.Err == nil && o.Offset >= 0 {
			out[tp.Partition] = min(o.Offset, end)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("topic %s doesn't exist", topic)
	}
	return out, nil
}
