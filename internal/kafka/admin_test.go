package kafka

import (
	"errors"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

// admin is a plain kadm client on c, standing in for another user.
func admin(t *testing.T, c interface{ ListenAddrs() []string }) *kadm.Client {
	t.Helper()
	kcl, err := kgo.NewClient(kgo.SeedBrokers(c.ListenAddrs()...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(kcl.Close)
	return kadm.NewClient(kcl)
}

func configOf(t *testing.T, cl Client, topic, name string) ConfigEntry {
	t.Helper()
	cfgs, err := cl.TopicConfigs(ctx(t), topic)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range cfgs {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no config %s", name)
	return ConfigEntry{}
}

func seedConfigs(t *testing.T, cl Client) {
	t.Helper()
	if err := cl.CreateTopic(ctx(t), NewTopic{Name: "audit", Partitions: 1, Replication: 1, Configs: map[string]string{"retention.ms": "1000", "cleanup.policy": "compact"}}); err != nil {
		t.Fatal(err)
	}
}

func TestAlterTopicConfigsIncremental(t *testing.T) {
	c := newCluster(t)
	cl := open(t, c)
	seedConfigs(t, cl)
	if err := cl.AlterTopicConfigs(ctx(t), "audit", []ConfigChange{{Name: "retention.ms", Value: "2000", Old: "1000"}}); err != nil {
		t.Fatal(err)
	}
	if e := configOf(t, cl, "audit", "retention.ms"); e.Value != "2000" {
		t.Fatalf("retention.ms = %+v", e)
	}
	if e := configOf(t, cl, "audit", "cleanup.policy"); e.Value != "compact" {
		t.Fatalf("cleanup.policy was overwritten: %+v", e)
	}
}

func TestAlterTopicConfigsDelete(t *testing.T) {
	c := newCluster(t)
	cl := open(t, c)
	seedConfigs(t, cl)
	if err := cl.AlterTopicConfigs(ctx(t), "audit", []ConfigChange{{Name: "retention.ms", Delete: true, Old: "1000"}}); err != nil {
		t.Fatal(err)
	}
	if e := configOf(t, cl, "audit", "retention.ms"); !e.Default {
		t.Fatalf("retention.ms not back to its default: %+v", e)
	}
}

func TestAlterTopicConfigsChanged(t *testing.T) {
	c := newCluster(t)
	cl := open(t, c)
	seedConfigs(t, cl)
	other := "5000"
	if _, err := admin(t, c).AlterTopicConfigs(ctx(t), []kadm.AlterConfig{{Op: kadm.SetConfig, Name: "retention.ms", Value: &other}}, "audit"); err != nil {
		t.Fatal(err)
	}
	err := cl.AlterTopicConfigs(ctx(t), "audit", []ConfigChange{{Name: "retention.ms", Value: "2000", Old: "1000"}})
	if !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("err = %v, want ErrConfigChanged", err)
	}
	if e := configOf(t, cl, "audit", "retention.ms"); e.Value != "5000" {
		t.Fatalf("retention.ms = %+v", e)
	}
}

func TestAddPartitions(t *testing.T) {
	c := newCluster(t)
	cl := open(t, c)
	if err := cl.AddPartitions(ctx(t), "orders", 5); err != nil {
		t.Fatal(err)
	}
	d, err := cl.TopicDetail(ctx(t), "orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Parts) != 5 {
		t.Fatalf("partitions = %d", len(d.Parts))
	}
	err = cl.AddPartitions(ctx(t), "orders", 4)
	if err == nil || !strings.Contains(err.Error(), "can only grow (orders has 5)") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteRecords(t *testing.T) {
	c := newCluster(t)
	fill(t, c, "orders", 1, 10)
	cl := open(t, c)
	res, err := cl.DeleteRecords(ctx(t), "orders", map[int32]int64{0: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Partition != 0 || res[0].Start != 4 || res[0].Err != nil {
		t.Fatalf("results = %+v", res)
	}
	d, err := cl.TopicDetail(ctx(t), "orders")
	if err != nil {
		t.Fatal(err)
	}
	if d.Parts[0].Start != 4 {
		t.Fatalf("p0 start = %d", d.Parts[0].Start)
	}
	_, err = cl.DeleteRecords(ctx(t), "orders", map[int32]int64{0: 20})
	var oor ErrOutOfRange
	if !errors.As(err, &oor) || err.Error() != "p0 has offsets 4 to 10 only" {
		t.Fatalf("err = %v", err)
	}
	if d, _ := cl.TopicDetail(ctx(t), "orders"); d.Parts[0].Start != 4 {
		t.Fatalf("an out-of-range delete was sent: start %d", d.Parts[0].Start)
	}
}

func TestDeleteGroupNotEmpty(t *testing.T) {
	c := newCluster(t)
	fill(t, c, "orders", 3, 2)
	member := consume(t, c, "billing", "orders")
	cl := open(t, c)
	if err := cl.DeleteGroup(ctx(t), "billing"); !errors.Is(err, ErrGroupNotEmpty) {
		t.Fatalf("err = %v, want ErrGroupNotEmpty", err)
	}
	member.Close()
	if err := cl.DeleteGroup(ctx(t), "billing"); err != nil {
		t.Fatal(err)
	}
	gs, err := cl.Groups(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		if g.Name == "billing" {
			t.Fatalf("billing is still there: %+v", gs)
		}
	}
}

func TestDeleteGroupOffsets(t *testing.T) {
	c := newCluster(t, kfake.SeedTopics(1, "refunds"))
	fill(t, c, "orders", 3, 2)
	fill(t, c, "refunds", 1, 2)
	commit(t, c, "billing",
		kadm.Offset{Topic: "orders", Partition: 0, At: 1, LeaderEpoch: -1},
		kadm.Offset{Topic: "refunds", Partition: 0, At: 1, LeaderEpoch: -1})
	cl := open(t, c)
	res, err := cl.DeleteGroupOffsets(ctx(t), "billing", "orders")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("result %+v", r)
		}
	}
	d, err := cl.GroupDetail(ctx(t), "billing")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range d.Lag {
		if l.Topic != "refunds" {
			t.Fatalf("lag left on %s: %+v", l.Topic, d.Lag)
		}
	}
	if len(d.Lag) == 0 {
		t.Fatal("refunds' offsets went too")
	}
}
