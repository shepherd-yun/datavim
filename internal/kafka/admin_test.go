package kafka

import (
	"errors"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kadm"
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
