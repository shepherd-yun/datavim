package kafka

import (
	"context"
	"errors"
	"fmt"

	"github.com/twmb/franz-go/pkg/kadm"
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
