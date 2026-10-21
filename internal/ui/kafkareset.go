package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

// groupResetMsg is a confirmed reset of the previewed partitions.
type groupResetMsg struct {
	tabID   int
	commits []kafka.OffsetCommit
}

func (m groupResetMsg) TabID() int { return m.tabID }

// groupResetDoneMsg is the outcome of a reset.
type groupResetDoneMsg struct {
	tabID   int
	conn    string
	results []kafka.CommitResult
	err     error
}

func (m groupResetDoneMsg) TabID() int               { return m.tabID }
func (m groupResetDoneMsg) kafkaMsg()                {}
func (m groupResetDoneMsg) failure() (string, error) { return m.conn, m.err }

// resetTimesMsg is the offsets at a time the reset dialog asked for.
type resetTimesMsg struct {
	at     time.Time
	byTime map[kafka.TP]int64
	err    error
}

// askReset is R: the reset dialog, unless the connection is read-only or
// the group's offsets cannot be reset.
func (t *groupTab) askReset() tea.Cmd {
	d := t.detail
	if d == nil {
		return nil
	}
	cfg, _ := t.backend.Connection(t.conn)
	if cfg.IsReadOnly() {
		t.setStatus(readOnlyHint, true)
		return nil
	}
	if !d.Type.HasLag() {
		t.setStatus(fmt.Sprintf("can't reset offsets of a %s group", d.Type), true)
		return nil
	}
	if len(d.Lag) == 0 {
		t.setStatus(t.group+" has no partitions to reset", true)
		return nil
	}
	var sel []kafka.TP
	if l, ok := t.currentLag(); ok {
		sel = []kafka.TP{l.TP}
	}
	id, b, conn := t.id, t.backend, t.conn
	topics := map[string]bool{}
	for _, l := range d.Lag {
		topics[l.Topic] = true
	}
	askTime := func(at time.Time) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
			defer cancel()
			msg := resetTimesMsg{at: at, byTime: map[kafka.TP]int64{}}
			msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
				for topic := range topics {
					offs, err := c.OffsetsAt(ctx, topic, at)
					if err != nil {
						return err
					}
					for p, o := range offs {
						msg.byTime[kafka.TP{Topic: topic, Partition: p}] = o
					}
				}
				return nil
			})
			return msg
		}
	}
	return openOverlay(overlay.NewResetOffsets(t.keys, t.group, d.Lag, sel, len(d.Members), cfg.Env == config.EnvProd, time.Now(),
		func(c []kafka.OffsetCommit) tea.Msg { return groupResetMsg{tabID: id, commits: c} }, askTime))
}

// reset commits the confirmed offsets; the session refuses it on a
// read-only connection, kafka when the group moved since the preview.
func (t *groupTab) reset(m groupResetMsg) tea.Cmd {
	id, b, conn, group := t.id, t.backend, t.conn, t.group
	t.setStatus("resetting offsets…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := groupResetDoneMsg{tabID: id, conn: conn}
		c, err := b.Kafka(ctx, conn)
		if err == nil {
			msg.results, err = c.ResetOffsets(ctx, group, m.commits)
		}
		msg.err = err
		return msg
	}
}

func (t *groupTab) resetDone(m groupResetDoneMsg) tea.Cmd {
	if m.err != nil {
		t.setStatus(kafkaErr(m.err), true)
		if errors.Is(m.err, kafka.ErrGroupChanged) || errors.Is(m.err, kafka.ErrGroupNotEmpty) {
			return t.load()
		}
		return nil
	}
	var failed []string
	for _, r := range m.results {
		if r.Err != nil {
			failed = append(failed, fmt.Sprintf("%s p%d: %s", r.Topic, r.Partition, kafkaErr(r.Err)))
		}
	}
	if len(failed) == 0 {
		t.setStatus(fmt.Sprintf("reset %s of %s", plural(len(m.results), "partition"), t.group), false)
	} else {
		t.setStatus(fmt.Sprintf("reset %d of %d partitions of %s · failed: %s", len(m.results)-len(failed), len(m.results), t.group,
			strings.Join(failed, "; ")), true)
	}
	return t.load()
}

// resetTimes hands the offsets at a time to the open reset dialog.
func (m *Model) resetTimes(msg resetTimesMsg) {
	r, ok := m.overlay.(*overlay.ResetOffsets)
	if !ok {
		return
	}
	if msg.err != nil {
		r.TimesFailed(msg.at, "offsets at "+msg.at.Format("2006-01-02 15:04")+": "+kafkaErr(msg.err))
		return
	}
	r.SetTimes(msg.at, msg.byTime)
}
