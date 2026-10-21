package ui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/kmsgx"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

type (
	// createTopicMsg is a confirmed new topic.
	createTopicMsg struct {
		conn  string
		topic kafka.NewTopic
	}
	// topicCreatedMsg is the outcome of a create.
	topicCreatedMsg struct {
		conn, name string
		err        error
	}
	// deleteTopicAskMsg is what the delete confirmation shows.
	deleteTopicAskMsg struct {
		conn, topic string
		parts       int
		msgs        int64
		groups      int
		err         error
	}
	// deleteTopicMsg is a confirmed delete.
	deleteTopicMsg struct{ conn, topic string }
	// topicDeletedMsg is the outcome of a delete.
	topicDeletedMsg struct {
		conn, topic string
		err         error
	}
)

func (m topicCreatedMsg) failure() (string, error)   { return m.conn, m.err }
func (m deleteTopicAskMsg) failure() (string, error) { return m.conn, m.err }
func (m topicDeletedMsg) failure() (string, error)   { return m.conn, m.err }

// refuseReadOnly says so and reports true on a read-only connection.
func (m *Model) refuseReadOnly(conn string) bool {
	if cfg, _ := m.backend.Connection(conn); cfg.IsReadOnly() {
		m.setStatus(readOnlyHint, true)
		return true
	}
	return false
}

// newTopicForm is o under Topics: the new-topic form.
func (m *Model) newTopicForm(n *tree.Node) tea.Cmd {
	if n.Section != secTopics {
		m.setStatus("select Topics to add a topic", true)
		return nil
	}
	if m.refuseReadOnly(n.Conn) {
		return nil
	}
	m.topicFormConn = n.Conn
	m.overlay = overlay.NewTopicForm(m.keys)
	return nil
}

// topicFormed confirms the new topic (design §8.2: y, prod or not).
func (m *Model) topicFormed(msg overlay.TopicFormMsg) {
	conn, t := m.topicFormConn, msg.Topic
	lines := []string{fmt.Sprintf("Create topic %s with %s on %s?", t.Name, plural(int(t.Partitions), "partition"), conn)}
	if t.Replication > 0 {
		lines = append(lines, fmt.Sprintf("replication %d", t.Replication))
	}
	for _, k := range slices.Sorted(maps.Keys(t.Configs)) {
		lines = append(lines, k+" = "+t.Configs[k])
	}
	cfg, _ := m.backend.Connection(conn)
	expect := ""
	if kmsgx.ConfirmFor(kmsgx.OpCreateTopic, cfg.Env == config.EnvProd) == kmsgx.ConfirmTypeName {
		expect = t.Name
	}
	m.overlay = overlay.NewConfirm("New topic", lines, expect, createTopicMsg{conn: conn, topic: t})
}

func (m *Model) createTopic(msg createTopicMsg) tea.Cmd {
	b := m.backend
	m.setStatus("creating topic "+msg.topic.Name+"…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		out := topicCreatedMsg{conn: msg.conn, name: msg.topic.Name}
		c, err := b.Kafka(ctx, msg.conn)
		if err == nil {
			err = c.CreateTopic(ctx, msg.topic)
		}
		out.err = err
		return out
	}
}

func (m *Model) topicCreated(msg topicCreatedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("create topic "+msg.name+": "+kafkaErr(msg.err), true)
		return nil
	}
	m.setStatus("created topic "+msg.name, false)
	if st := m.kafkaTrees[msg.conn]; st != nil {
		st.selectTopic = msg.name
	}
	return m.reloadKafkaTree(msg.conn)
}

// reloadKafkaTree reads conn's tree again.
func (m *Model) reloadKafkaTree(conn string) tea.Cmd {
	root := m.kafkaRoot(conn)
	if root == nil || root.Loading {
		return nil
	}
	m.tree.StartLoad(root)
	return loadKafkaTree(m.backend, root)
}

// askDeleteTopic is dd on a topic: what it holds, then the confirmation.
func (m *Model) askDeleteTopic(n *tree.Node) tea.Cmd {
	if m.refuseReadOnly(n.Conn) {
		return nil
	}
	b, conn, topic := m.backend, n.Conn, n.Key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := deleteTopicAskMsg{conn: conn, topic: topic}
		msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
			d, err := c.TopicDetail(ctx, topic)
			if err != nil {
				return err
			}
			msg.parts = len(d.Parts)
			for _, p := range d.Parts {
				msg.msgs += max(p.End-p.Start, 0)
			}
			groups, err := c.TopicGroups(ctx, topic)
			if err != nil {
				return err
			}
			msg.groups = len(groups)
			return nil
		})
		return msg
	}
}

func (m *Model) deleteTopicAsked(msg deleteTopicAskMsg) {
	if msg.err != nil {
		m.setStatus("delete topic "+msg.topic+": "+kafkaErr(msg.err), true)
		return
	}
	line := fmt.Sprintf("Delete topic %s? %s · %s messages · consumed by %s · this can't be undone.",
		msg.topic, plural(msg.parts, "partition"), humanCount(msg.msgs), plural(msg.groups, "group"))
	// a dangerous operation: the name is typed on every connection
	m.overlay = overlay.NewConfirm("Delete topic", []string{line, "Type the topic name to confirm."}, msg.topic,
		deleteTopicMsg{conn: msg.conn, topic: msg.topic})
}

func (m *Model) deleteTopic(msg deleteTopicMsg) tea.Cmd {
	b := m.backend
	m.setStatus("deleting topic "+msg.topic+"…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		out := topicDeletedMsg{conn: msg.conn, topic: msg.topic}
		c, err := b.Kafka(ctx, msg.conn)
		if err == nil {
			err = c.DeleteTopic(ctx, msg.topic)
		}
		out.err = err
		return out
	}
}

// topicDeleted closes the topic's tab and producer console (the draft is
// kept) and reads the tree again.
func (m *Model) topicDeleted(msg topicDeletedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("delete topic "+msg.topic+": "+kafkaErr(msg.err), true)
		return nil
	}
	m.setStatus("deleted topic "+msg.topic, false)
	cmds := []tea.Cmd{m.reloadKafkaTree(msg.conn)}
	for _, key := range []string{topicTabKey(msg.conn, msg.topic), producerTabKey(msg.conn, msg.topic)} {
		if i := slices.IndexFunc(m.tabs, func(t tab) bool { return t.Key() == key }); i >= 0 {
			cmds = append(cmds, m.closeTab(i))
		}
	}
	return tea.Batch(cmds...)
}

// selectNewTopic opens the way to topic and selects it.
func (m *Model) selectNewTopic(conn, topic string) {
	for _, r := range m.tree.Roots() {
		if r.Conn != conn {
			continue
		}
		for _, sec := range r.Children {
			if sec.Section != secTopics {
				continue
			}
			for _, c := range sec.Children {
				if c.Key == topic || c.Kind == tree.KindTopicFolder && strings.HasPrefix(topic, c.Key) &&
					slices.ContainsFunc(c.Children, func(n *tree.Node) bool { return n.Key == topic }) {
					sec.Expanded, c.Expanded = true, true
				}
			}
		}
	}
	m.tree.SelectWhere(func(n *tree.Node) bool { return n.Conn == conn && n.Kind == tree.KindTopic && n.Key == topic })
}
