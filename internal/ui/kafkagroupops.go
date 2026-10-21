package ui

import (
	"context"
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/kmsgx"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

type (
	// deleteGroupAskMsg is the group a dd on its node wants to delete, read again.
	deleteGroupAskMsg struct {
		conn, group string
		detail      kafka.GroupDetail
		err         error
	}
	// deleteGroupMsg is the confirmed delete of a group.
	deleteGroupMsg struct{ conn, group string }
	// groupDeletedMsg is the outcome of DeleteGroup.
	groupDeletedMsg struct {
		conn, group string
		err         error
	}
	// groupDelOffsetsMsg is the confirmed delete of a group's offsets on topic.
	groupDelOffsetsMsg struct {
		tabID int
		topic string
	}
	// groupDelOffsetsDoneMsg is the outcome of DeleteGroupOffsets.
	groupDelOffsetsDoneMsg struct {
		tabID   int
		conn    string
		topic   string
		results []kafka.CommitResult
		err     error
	}
)

func (m deleteGroupAskMsg) failure() (string, error)      { return m.conn, m.err }
func (m groupDeletedMsg) failure() (string, error)        { return m.conn, m.err }
func (m groupDelOffsetsMsg) TabID() int                   { return m.tabID }
func (m groupDelOffsetsDoneMsg) TabID() int               { return m.tabID }
func (m groupDelOffsetsDoneMsg) kafkaMsg()                {}
func (m groupDelOffsetsDoneMsg) failure() (string, error) { return m.conn, m.err }

// lagTopics is the distinct topics of lag, in order.
func lagTopics(lag []kafka.PartitionLag) []string {
	var out []string
	for _, l := range lag {
		if !slices.Contains(out, l.Topic) {
			out = append(out, l.Topic)
		}
	}
	return out
}

// askDeleteGroup is dd on a group: its state read again, then the
// confirmation.
func (m *Model) askDeleteGroup(n *tree.Node) tea.Cmd {
	if m.refuseReadOnly(n.Conn) {
		return nil
	}
	b, conn, group := m.backend, n.Conn, n.Key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := deleteGroupAskMsg{conn: conn, group: group}
		msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
			var err error
			msg.detail, err = c.GroupDetail(ctx, group)
			return err
		})
		return msg
	}
}

func (m *Model) deleteGroupAsked(msg deleteGroupAskMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("delete group "+msg.group+": "+kafkaErr(msg.err), true)
		return nil
	}
	d := msg.detail
	if !d.Type.HasLag() {
		m.setStatus(fmt.Sprintf("can't delete a %s group here", d.Type), true)
		return nil
	}
	if n := len(d.Members); n > 0 || (d.State != "Empty" && d.State != "Dead") {
		m.setStatus(fmt.Sprintf("group has %s — stop the consumers first", plural(n, "active member")), true)
		return nil
	}
	line := fmt.Sprintf("Delete group %s? It has committed offsets on %s · this can't be undone.",
		msg.group, plural(len(lagTopics(d.Lag)), "topic"))
	cmd, why := kafkaConfirm(m.backend, msg.conn, kmsgx.OpDeleteGroup, "Delete group", []string{line}, msg.group,
		deleteGroupMsg{conn: msg.conn, group: msg.group})
	if why != "" {
		m.setStatus(why, true)
	}
	return cmd
}

func (m *Model) deleteGroup(msg deleteGroupMsg) tea.Cmd {
	b := m.backend
	m.setStatus("deleting group "+msg.group+"…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		out := groupDeletedMsg{conn: msg.conn, group: msg.group}
		c, err := b.Kafka(ctx, msg.conn)
		if err == nil {
			err = c.DeleteGroup(ctx, msg.group)
		}
		out.err = err
		return out
	}
}

// groupDeleted closes the group's tab and reads the tree again.
func (m *Model) groupDeleted(msg groupDeletedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("delete group "+msg.group+": "+kafkaErr(msg.err), true)
		return nil
	}
	m.setStatus("deleted group "+msg.group, false)
	cmds := []tea.Cmd{m.reloadKafkaTree(msg.conn)}
	if i := slices.IndexFunc(m.tabs, func(t tab) bool { return t.Key() == groupTabKey(msg.conn, msg.group) }); i >= 0 {
		cmds = append(cmds, m.closeTab(i))
	}
	return tea.Batch(cmds...)
}

// askDeleteOffsets is X on a lag row: delete the group's offsets on its topic.
func (t *groupTab) askDeleteOffsets() tea.Cmd {
	l, ok := t.currentLag()
	if t.section != secLag || !ok {
		return nil
	}
	parts := 0
	for _, x := range t.detail.Lag {
		if x.Topic == l.Topic {
			parts++
		}
	}
	line := fmt.Sprintf("Delete %s's offsets on %s (%s)? The group starts from its auto.offset.reset next time.",
		t.group, l.Topic, plural(parts, "partition"))
	cmd, why := kafkaConfirm(t.backend, t.conn, kmsgx.OpDeleteOffsets, "Delete offsets", []string{line}, t.group,
		groupDelOffsetsMsg{tabID: t.id, topic: l.Topic})
	if why != "" {
		t.setStatus(why, true)
	}
	return cmd
}

func (t *groupTab) deleteOffsets(topic string) tea.Cmd {
	t.setStatus("deleting offsets on "+topic+"…", false)
	id, b, conn, group := t.id, t.backend, t.conn, t.group
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := groupDelOffsetsDoneMsg{tabID: id, conn: conn, topic: topic}
		c, err := b.Kafka(ctx, conn)
		if err == nil {
			msg.results, err = c.DeleteGroupOffsets(ctx, group, topic)
		}
		msg.err = err
		return msg
	}
}

func (t *groupTab) offsetsDeleted(msg groupDelOffsetsDoneMsg) tea.Cmd {
	if msg.err != nil {
		t.setStatus("delete offsets on "+msg.topic+": "+kafkaErr(msg.err), true)
		return nil
	}
	failed := 0
	var first error
	for _, r := range msg.results {
		if r.Err != nil {
			failed++
			if first == nil {
				first = r.Err
			}
		}
	}
	if failed > 0 {
		t.setStatus(fmt.Sprintf("deleted %d of %d offsets on %s · %s", len(msg.results)-failed, len(msg.results), msg.topic, kafkaErr(first)), true)
	} else {
		t.setStatus("deleted the offsets on "+msg.topic, false)
	}
	if t.loading {
		return nil
	}
	return t.load()
}
