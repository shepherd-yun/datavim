package ui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/session"
)

// fakeKafkaBase is the time of offset 0 of every fake partition; offset o
// of partition p is at base + o*3s + p*1s, so partitions interleave.
var fakeKafkaBase = time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)

type fakeTopic struct {
	detail  kafka.TopicDetail
	configs []kafka.ConfigEntry
	recs    map[int32][]kafka.Record // by offset - start
}

type fakeProduce struct {
	topic string
	recs  []kafka.ProduceRecord
}

type fakeReset struct {
	group   string
	commits []kafka.OffsetCommit
}

// fakeKafka is an in-memory cluster shared by the clients and readers
// fakeBackend hands out; it records every call.
type fakeKafka struct {
	mu          sync.Mutex
	cluster     kafka.ClusterInfo
	topics      map[string]*fakeTopic
	groups      map[string]kafka.GroupDetail
	lagCalls    [][]string // the groups of every GroupsLag call
	calls       []string   // "Method arg"
	errs        map[string]error
	produced    []fakeProduce
	created     []kafka.NewTopic
	deleted     []string
	resets      []fakeReset
	readers     []*fakeReader
	tailQ       []kafka.Record // what Poll hands out next
	unsupported map[string]bool
	acls        []kafka.ACL
	produceErr  error
	resetResult func([]kafka.OffsetCommit) ([]kafka.CommitResult, error)
	alters      [][]kafka.ConfigChange // AlterTopicConfigs calls
	deletes     []map[int32]int64      // DeleteRecords calls
}

func newFakeKafka() *fakeKafka {
	return &fakeKafka{
		cluster: kafka.ClusterInfo{ID: "abc", Controller: 1, Version: "4.1", Brokers: []kafka.Broker{
			{ID: 1, Host: "b1", Port: 9092, Rack: "a", Controller: true, Leaders: 2},
			{ID: 2, Host: "b2", Port: 9092, Rack: "b", Leaders: 1},
		}},
		topics: map[string]*fakeTopic{},
		groups: map[string]kafka.GroupDetail{},
		errs:   map[string]error{},
	}
}

// addTopic adds a topic with n records (offsets start..start+n) on each of
// its parts partitions; value is {"p":P,"o":O}, key k-O.
func (k *fakeKafka) addTopic(name string, parts int, start, n int64) *fakeTopic {
	k.mu.Lock()
	defer k.mu.Unlock()
	t := &fakeTopic{recs: map[int32][]kafka.Record{}}
	t.detail = kafka.TopicDetail{Topic: kafka.Topic{Name: name, Partitions: parts, Replication: 2}}
	for p := range int32(parts) {
		t.detail.Parts = append(t.detail.Parts, kafka.Partition{ID: p, Leader: 1, Replicas: []int32{1, 2}, ISR: []int32{1, 2}, Start: start, End: start + n})
		for o := start; o < start+n; o++ {
			t.recs[p] = append(t.recs[p], fakeRecord(name, p, o))
		}
	}
	t.configs = []kafka.ConfigEntry{
		{Name: "cleanup.policy", Value: "delete", Source: "default", Default: true},
		{Name: "retention.ms", Value: "604800000", Source: "topic"},
	}
	k.topics[name] = t
	return t
}

func fakeRecord(topic string, p int32, o int64) kafka.Record {
	v := fmt.Sprintf(`{"p":%d,"o":%d}`, p, o)
	key := "k-" + strconv.FormatInt(o, 10)
	return kafka.Record{
		Topic: topic, Partition: p, Offset: o, TimeType: "CreateTime",
		Time: fakeKafkaBase.Add(time.Duration(o)*3*time.Second + time.Duration(p)*time.Second),
		Key:  []byte(key), Value: []byte(v), Size: len(key) + len(v),
	}
}

// setRecord replaces one record of a topic.
func (k *fakeKafka) setRecord(r kafka.Record) {
	k.mu.Lock()
	defer k.mu.Unlock()
	t := k.topics[r.Topic]
	start := t.detail.Parts[r.Partition].Start
	t.recs[r.Partition][r.Offset-start] = r
}

func (k *fakeKafka) addGroup(d kafka.GroupDetail) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if d.Type == "" {
		d.Type = kafka.GroupClassic
	}
	if d.State == "" {
		d.State = "Stable"
	}
	k.groups[d.Name] = d
}

func (k *fakeKafka) call(s string) {
	k.calls = append(k.calls, s)
}

// callsOf is the calls that start with prefix.
func (k *fakeKafka) callsOf(prefix string) []string {
	var out []string
	for _, c := range k.callList() {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func (k *fakeKafka) callList() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.calls)
}

func (k *fakeKafka) countCalls(prefix string) int {
	n := 0
	for _, c := range k.callList() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (k *fakeKafka) setErr(method string, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.errs[method] = err
}

// fakeKafkaClient is one handed-out client; ro makes writes fail like the
// session guard does.
type fakeKafkaClient struct {
	k  *fakeKafka
	ro bool
}

func (c *fakeKafkaClient) Cluster(context.Context) (kafka.ClusterInfo, error) {
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("Cluster")
	return k.cluster, k.errs["Cluster"]
}

func (c *fakeKafkaClient) Topics(context.Context) ([]kafka.Topic, error) {
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("Topics")
	var out []kafka.Topic
	for _, t := range k.topics {
		out = append(out, t.detail.Topic)
	}
	slices.SortFunc(out, func(a, b kafka.Topic) int { return strings.Compare(a.Name, b.Name) })
	return out, k.errs["Topics"]
}

func (c *fakeKafkaClient) TopicDetail(_ context.Context, topic string) (kafka.TopicDetail, error) {
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("TopicDetail " + topic)
	if err := k.errs["TopicDetail"]; err != nil {
		return kafka.TopicDetail{}, err
	}
	t, ok := k.topics[topic]
	if !ok {
		return kafka.TopicDetail{}, fmt.Errorf("topic %s doesn't exist", topic)
	}
	d := t.detail
	d.Parts = slices.Clone(d.Parts)
	return d, nil
}

func (c *fakeKafkaClient) TopicConfigs(_ context.Context, topic string) ([]kafka.ConfigEntry, error) {
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("TopicConfigs " + topic)
	if err := k.errs["TopicConfigs"]; err != nil {
		return nil, err
	}
	return slices.Clone(k.topics[topic].configs), nil
}

func (c *fakeKafkaClient) Supports(feature string) bool {
	c.k.mu.Lock()
	defer c.k.mu.Unlock()
	return !c.k.unsupported[feature]
}

func (c *fakeKafkaClient) Groups(context.Context) ([]kafka.Group, error) {
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("Groups")
	var out []kafka.Group
	for _, d := range k.groups {
		g := d.Group
		g.Protocol, g.Members = "", 0
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b kafka.Group) int { return strings.Compare(a.Name, b.Name) })
	return out, k.errs["Groups"]
}

func (c *fakeKafkaClient) GroupDetail(_ context.Context, group string) (kafka.GroupDetail, error) {
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("GroupDetail " + group)
	if err := k.errs["GroupDetail"]; err != nil {
		return kafka.GroupDetail{}, err
	}
	d, ok := k.groups[group]
	if !ok {
		return kafka.GroupDetail{}, fmt.Errorf("group %s doesn't exist", group)
	}
	d.Lag = slices.Clone(d.Lag)
	slices.SortFunc(d.Lag, func(a, b kafka.PartitionLag) int {
		return cmp.Or(cmp.Compare(b.Lag, a.Lag), strings.Compare(a.Topic, b.Topic), cmp.Compare(a.Partition, b.Partition))
	})
	d.Group.Members = len(d.Members)
	return d, nil
}

func groupLagSum(d kafka.GroupDetail) int64 {
	var n int64
	for _, l := range d.Lag {
		n += l.Lag
	}
	return n
}

func (c *fakeKafkaClient) GroupsLag(_ context.Context, groups []string) (map[string]int64, error) {
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("GroupsLag")
	k.lagCalls = append(k.lagCalls, slices.Clone(groups))
	out := map[string]int64{}
	for _, g := range groups {
		if d, ok := k.groups[g]; ok && d.Type.HasLag() {
			out[g] = groupLagSum(d)
		}
	}
	return out, k.errs["GroupsLag"]
}

func (c *fakeKafkaClient) TopicGroups(_ context.Context, topic string) ([]kafka.GroupDetail, error) {
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("TopicGroups " + topic)
	if err := k.errs["TopicGroups"]; err != nil {
		return nil, err
	}
	var out []kafka.GroupDetail
	for _, d := range k.groups {
		var lag []kafka.PartitionLag
		for _, l := range d.Lag {
			if l.Topic == topic {
				lag = append(lag, l)
			}
		}
		if len(lag) > 0 {
			d.Lag, d.Members = lag, nil
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b kafka.GroupDetail) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (c *fakeKafkaClient) OffsetsAt(_ context.Context, topic string, ts time.Time) (map[int32]int64, error) {
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("OffsetsAt " + topic)
	t := k.topics[topic]
	out := map[int32]int64{}
	for _, p := range t.detail.Parts {
		out[p.ID] = p.End
		for _, r := range t.recs[p.ID] {
			if !r.Time.Before(ts) {
				out[p.ID] = r.Offset
				break
			}
		}
	}
	return out, nil
}

func (c *fakeKafkaClient) Produce(_ context.Context, topic string, recs []kafka.ProduceRecord) ([]kafka.ProduceResult, error) {
	if c.ro {
		return nil, session.ErrReadOnly
	}
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("Produce " + topic)
	k.produced = append(k.produced, fakeProduce{topic, slices.Clone(recs)})
	if k.produceErr != nil {
		return nil, k.produceErr
	}
	t, ok := k.topics[topic]
	if !ok {
		return nil, fmt.Errorf("topic %s doesn't exist", topic)
	}
	var out []kafka.ProduceResult
	for _, r := range recs {
		p := max(r.Partition, 0)
		part := &t.detail.Parts[p]
		out = append(out, kafka.ProduceResult{Partition: p, Offset: part.End, Time: fakeKafkaBase})
		part.End++
	}
	return out, nil
}

func (c *fakeKafkaClient) CreateTopic(_ context.Context, nt kafka.NewTopic) error {
	if c.ro {
		return session.ErrReadOnly
	}
	k := c.k
	k.mu.Lock()
	k.call("CreateTopic " + nt.Name)
	k.created = append(k.created, nt)
	err := k.errs["CreateTopic"]
	k.mu.Unlock()
	if err == nil {
		k.addTopic(nt.Name, int(nt.Partitions), 0, 0)
	}
	return err
}

func (c *fakeKafkaClient) DeleteTopic(_ context.Context, name string) error {
	if c.ro {
		return session.ErrReadOnly
	}
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("DeleteTopic " + name)
	k.deleted = append(k.deleted, name)
	delete(k.topics, name)
	return k.errs["DeleteTopic"]
}

func (c *fakeKafkaClient) ResetOffsets(_ context.Context, group string, commits []kafka.OffsetCommit) ([]kafka.CommitResult, error) {
	if c.ro {
		return nil, session.ErrReadOnly
	}
	k := c.k
	k.mu.Lock()
	k.call("ResetOffsets " + group)
	k.resets = append(k.resets, fakeReset{group, slices.Clone(commits)})
	if f := k.resetResult; f != nil {
		k.mu.Unlock()
		return f(commits)
	}
	defer k.mu.Unlock()
	if err := k.errs["ResetOffsets"]; err != nil {
		return nil, err
	}
	d := k.groups[group]
	var out []kafka.CommitResult
	for _, cm := range commits {
		for i, l := range d.Lag {
			if l.TP == cm.TP {
				d.Lag[i].Committed, d.Lag[i].Lag = cm.To, l.End-cm.To
			}
		}
		out = append(out, kafka.CommitResult{TP: cm.TP, Offset: cm.To})
	}
	k.groups[group] = d
	return out, nil
}

func (c *fakeKafkaClient) AlterTopicConfigs(_ context.Context, topic string, changes []kafka.ConfigChange) error {
	if c.ro {
		return session.ErrReadOnly
	}
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("AlterTopicConfigs " + topic)
	k.alters = append(k.alters, slices.Clone(changes))
	return k.errs["AlterTopicConfigs"]
}

func (c *fakeKafkaClient) AddPartitions(_ context.Context, topic string, total int) error {
	if c.ro {
		return session.ErrReadOnly
	}
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call(fmt.Sprintf("AddPartitions %s %d", topic, total))
	return k.errs["AddPartitions"]
}

func (c *fakeKafkaClient) DeleteRecords(_ context.Context, topic string, before map[int32]int64) ([]kafka.DeleteResult, error) {
	if c.ro {
		return nil, session.ErrReadOnly
	}
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("DeleteRecords " + topic)
	k.deletes = append(k.deletes, maps.Clone(before))
	if err := k.errs["DeleteRecords"]; err != nil {
		return nil, err
	}
	var out []kafka.DeleteResult
	for p, o := range before {
		if t := k.topics[topic]; t != nil && int(p) < len(t.detail.Parts) {
			t.detail.Parts[p].Start = o
		}
		out = append(out, kafka.DeleteResult{Partition: p, Start: o})
	}
	slices.SortFunc(out, func(a, b kafka.DeleteResult) int { return int(a.Partition - b.Partition) })
	return out, nil
}

func (c *fakeKafkaClient) ACLs(context.Context) ([]kafka.ACL, error) {
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("ACLs")
	return slices.Clone(k.acls), k.errs["ACLs"]
}

func (c *fakeKafkaClient) CreateACLs(_ context.Context, acls []kafka.ACL) error {
	if c.ro {
		return session.ErrReadOnly
	}
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, a := range acls {
		k.call("CreateACLs " + a.String())
	}
	if err := k.errs["CreateACLs"]; err != nil {
		return err
	}
	k.acls = append(k.acls, acls...)
	return nil
}

func (c *fakeKafkaClient) DeleteACLs(_ context.Context, acls []kafka.ACL) ([]kafka.ACL, error) {
	if c.ro {
		return nil, session.ErrReadOnly
	}
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, a := range acls {
		k.call("DeleteACLs " + a.String())
	}
	if err := k.errs["DeleteACLs"]; err != nil {
		return nil, err
	}
	k.acls = slices.DeleteFunc(k.acls, func(a kafka.ACL) bool { return slices.Contains(acls, a) })
	return acls, nil
}

func (c *fakeKafkaClient) DeleteGroup(_ context.Context, group string) error {
	if c.ro {
		return session.ErrReadOnly
	}
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("DeleteGroup " + group)
	if err := k.errs["DeleteGroup"]; err != nil {
		return err
	}
	delete(k.groups, group)
	return nil
}

func (c *fakeKafkaClient) DeleteGroupOffsets(_ context.Context, group, topic string) ([]kafka.CommitResult, error) {
	if c.ro {
		return nil, session.ErrReadOnly
	}
	k := c.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.call("DeleteGroupOffsets " + group + " " + topic)
	if err := k.errs["DeleteGroupOffsets"]; err != nil {
		return nil, err
	}
	d := k.groups[group]
	var out []kafka.CommitResult
	d.Lag = slices.DeleteFunc(d.Lag, func(l kafka.PartitionLag) bool {
		if l.Topic == topic {
			out = append(out, kafka.CommitResult{TP: l.TP, Offset: -1})
			return true
		}
		return false
	})
	k.groups[group] = d
	return out, nil
}

func (c *fakeKafkaClient) Close() {}

// fakeReader reads the fake cluster and records its calls.
type fakeReader struct {
	k       *fakeKafka
	rc      kafka.ReaderConfig
	mu      sync.Mutex
	ranges  []map[int32]kafka.Range
	tails   []map[int32]int64
	polls   int
	closed  bool
	tailing bool          // Range and Close stop live tail, as the real reader does
	block   chan struct{} // Range waits on it when set
}

func (r *fakeReader) Range(ctx context.Context, topic string, ranges map[int32]kafka.Range) ([]kafka.Record, error) {
	r.mu.Lock()
	r.ranges = append(r.ranges, ranges)
	r.tailing = false
	block := r.block
	r.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	k := r.k
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.errs["Range"]; err != nil {
		return nil, err
	}
	t := k.topics[topic]
	var out []kafka.Record
	for p, rg := range ranges {
		start := t.detail.Parts[p].Start
		for _, rec := range t.recs[p] {
			if rec.Offset >= rg.From && rec.Offset < rg.To && rec.Offset >= start {
				out = append(out, rec)
			}
		}
	}
	slices.SortFunc(out, func(a, b kafka.Record) int {
		return cmp.Or(cmp.Compare(a.Partition, b.Partition), cmp.Compare(a.Offset, b.Offset))
	})
	return out, nil
}

func (r *fakeReader) Tail(_ context.Context, _ string, from map[int32]int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tails = append(r.tails, from)
	r.tailing = true
	return nil
}

func (r *fakeReader) Poll(context.Context, int) ([]kafka.Record, error) {
	r.mu.Lock()
	r.polls++
	tailing := r.tailing
	r.mu.Unlock()
	if !tailing {
		return nil, errors.New("kafka: not tailing")
	}
	r.k.mu.Lock()
	defer r.k.mu.Unlock()
	out := r.k.tailQ
	r.k.tailQ = nil
	return out, nil
}

func (r *fakeReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed, r.tailing = true, false
	return nil
}

func (r *fakeReader) rangeList() []map[int32]kafka.Range {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ranges)
}

func (r *fakeReader) pollCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.polls
}

func (r *fakeReader) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// addKafka adds a kafka connection backed by f.kafka (created on first use).
func (f *fakeBackend) addKafka(cfg config.Connection) *fakeKafka {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cfg.Driver == "" {
		cfg.Driver = "kafka"
	}
	if cfg.Env == "" {
		cfg.Env = config.EnvDev
	}
	if f.kafka == nil {
		f.kafka = newFakeKafka()
	}
	f.conns = append(f.conns, cfg)
	return f.kafka
}

func (f *fakeBackend) kafkaConn(name string) (config.Connection, error) {
	if f.connErr != nil {
		return config.Connection{}, f.connErr
	}
	if f.needPassword {
		return config.Connection{}, session.ErrPasswordRequired
	}
	for _, c := range f.conns {
		if c.Name == name && c.Driver == "kafka" && f.kafka != nil {
			return c, nil
		}
	}
	return config.Connection{}, fmt.Errorf("connection %q is not a kafka connection", name)
}

func (f *fakeBackend) Kafka(_ context.Context, name string) (kafka.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kafkaOpens = append(f.kafkaOpens, name)
	cfg, err := f.kafkaConn(name)
	if err != nil {
		return nil, err
	}
	return &fakeKafkaClient{k: f.kafka, ro: cfg.IsReadOnly()}, nil
}

func (f *fakeBackend) KafkaReader(_ context.Context, name string, rc kafka.ReaderConfig) (kafka.Reader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.kafkaConn(name); err != nil {
		return nil, err
	}
	r := &fakeReader{k: f.kafka, rc: rc}
	f.kafka.mu.Lock()
	f.kafka.readers = append(f.kafka.readers, r)
	f.kafka.mu.Unlock()
	return r, nil
}

// lastReader is the newest reader handed out.
func (k *fakeKafka) lastReader() *fakeReader {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.readers) == 0 {
		return nil
	}
	return k.readers[len(k.readers)-1]
}
