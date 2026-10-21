package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/kmsgx"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

type (
	// addPartsMsg is the new partition total typed in the prompt.
	addPartsMsg struct {
		tabID, total int
	}
	// addPartsGoMsg is the confirmed AddPartitions.
	addPartsGoMsg struct {
		tabID, total int
	}
	// addPartsDoneMsg is the outcome of AddPartitions.
	addPartsDoneMsg struct {
		tabID int
		conn  string
		total int
		err   error
	}
	// delScopeMsg is the partitions a delete of messages works on.
	delScopeMsg struct {
		tabID int
		scope []int32
	}
	// delTargetMsg is the delete target typed for scope.
	delTargetMsg struct {
		tabID  int
		scope  []int32
		target kmsgx.DeleteTarget
	}
	// delPlanMsg is a delete's preview, read with the topic's bounds of now.
	delPlanMsg struct {
		tabID int
		conn  string
		rows  []kmsgx.DeleteRow
		err   error
	}
	// delGoMsg is the confirmed delete.
	delGoMsg struct {
		tabID int
		rows  []kmsgx.DeleteRow
	}
	// delDoneMsg is the outcome of DeleteRecords.
	delDoneMsg struct {
		tabID   int
		conn    string
		results []kafka.DeleteResult
		err     error
	}
)

func (m addPartsMsg) TabID() int                   { return m.tabID }
func (m addPartsGoMsg) TabID() int                 { return m.tabID }
func (m addPartsDoneMsg) TabID() int               { return m.tabID }
func (m addPartsDoneMsg) kafkaMsg()                {}
func (m addPartsDoneMsg) failure() (string, error) { return m.conn, m.err }
func (m delScopeMsg) TabID() int                   { return m.tabID }
func (m delTargetMsg) TabID() int                  { return m.tabID }
func (m delPlanMsg) TabID() int                    { return m.tabID }
func (m delPlanMsg) kafkaMsg()                     {}
func (m delPlanMsg) failure() (string, error)      { return m.conn, m.err }
func (m delGoMsg) TabID() int                      { return m.tabID }
func (m delDoneMsg) TabID() int                    { return m.tabID }
func (m delDoneMsg) kafkaMsg()                     {}
func (m delDoneMsg) failure() (string, error)      { return m.conn, m.err }

// refuseReadOnly says so and reports true on a read-only connection, before
// any popup opens.
func (t *topicTab) refuseReadOnly() bool {
	if cfg, _ := t.backend.Connection(t.conn); cfg.IsReadOnly() {
		t.setStatus(readOnlyHint, true)
		return true
	}
	return false
}

// addPartitionsPrompt is o in the Partitions section.
func (t *topicTab) addPartitionsPrompt() tea.Cmd {
	if t.refuseReadOnly() || t.detail == nil {
		return nil
	}
	now, id := len(t.detail.Parts), t.id
	return openOverlay(overlay.NewPrompt("Add partitions", fmt.Sprintf("New total (now %d)", now), "",
		func(s string) tea.Msg {
			n, _ := strconv.Atoi(strings.TrimSpace(s))
			return addPartsMsg{tabID: id, total: n}
		},
		overlay.WithValidate(func(s string) error {
			if n, err := strconv.Atoi(strings.TrimSpace(s)); err != nil || n <= now {
				return fmt.Errorf("a number above %d: partitions can only be added", now)
			}
			return nil
		})))
}

func (t *topicTab) confirmAddPartitions(total int) tea.Cmd {
	lines := []string{
		fmt.Sprintf("%s: %d → %d partitions", t.topic, len(t.detail.Parts), total),
		"Messages with a key may go to a different partition from now on.",
	}
	cmd, why := kafkaConfirm(t.backend, t.conn, kmsgx.OpAddPartitions, "Add partitions", lines, t.topic, addPartsGoMsg{tabID: t.id, total: total})
	if why != "" {
		t.setStatus(why, true)
	}
	return cmd
}

func (t *topicTab) addPartitions(total int) tea.Cmd {
	t.setStatus("adding partitions…", false)
	tabCtx, id, b, conn, topic := t.ctx, t.id, t.backend, t.conn, t.topic
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, queryTimeout)
		defer cancel()
		c, err := b.Kafka(ctx, conn)
		if err == nil {
			err = c.AddPartitions(ctx, topic, total)
		}
		return addPartsDoneMsg{tabID: id, conn: conn, total: total, err: err}
	}
}

// deleteFromPartitions is X in the Partitions section: the cursor's
// partition or all of them, then the target.
func (t *topicTab) deleteFromPartitions() tea.Cmd {
	if t.refuseReadOnly() || t.detail == nil || len(t.detail.Parts) == 0 {
		return nil
	}
	r, _ := t.partGrid.Position()
	r = min(max(r, 0), len(t.detail.Parts)-1)
	one := t.detail.Parts[r].ID
	all := make([]int32, len(t.detail.Parts))
	for i, p := range t.detail.Parts {
		all[i] = p.ID
	}
	id := t.id
	return openOverlay(overlay.NewChoice("Delete messages", []string{
		fmt.Sprintf("p%d only", one), "all " + plural(len(all), "partition"),
	}, func(i int) tea.Msg {
		if i == 0 {
			return delScopeMsg{tabID: id, scope: []int32{one}}
		}
		return delScopeMsg{tabID: id, scope: all}
	}))
}

func (t *topicTab) deleteTargetPrompt(scope []int32) tea.Cmd {
	id := t.id
	parse := func(s string) (kmsgx.DeleteTarget, error) { return kmsgx.ParseDeleteTarget(s, time.Now()) }
	return openOverlay(overlay.NewPrompt("Delete messages", "Delete messages before (an offset, -N keeps the newest N, @time, all)", "",
		func(s string) tea.Msg {
			tg, _ := parse(s)
			return delTargetMsg{tabID: id, scope: scope, target: tg}
		},
		overlay.WithValidate(func(s string) error { _, err := parse(s); return err })))
}

// deleteFromMessage is X on a message: delete its partition up to and
// including it.
func (t *topicTab) deleteFromMessage() tea.Cmd {
	if t.refuseReadOnly() {
		return nil
	}
	r, ok := t.msgs.current()
	if !ok {
		return nil
	}
	return t.planDelete([]int32{r.Partition}, kmsgx.DeleteTarget{Kind: kmsgx.DeleteBefore, Offset: r.Offset + 1})
}

// planDelete reads the topic's bounds (and the offsets of a time) and
// previews the delete.
func (t *topicTab) planDelete(scope []int32, target kmsgx.DeleteTarget) tea.Cmd {
	tabCtx, id, b, conn, topic := t.ctx, t.id, t.backend, t.conn, t.topic
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		msg := delPlanMsg{tabID: id, conn: conn}
		msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
			if !c.Supports("delete_records") {
				return kafka.ErrUnsupported
			}
			d, err := c.TopicDetail(ctx, topic)
			if err != nil {
				return err
			}
			var byTime map[int32]int64
			if target.Kind == kmsgx.DeleteBeforeTime {
				if byTime, err = c.OffsetsAt(ctx, topic, target.Time); err != nil {
					return err
				}
			}
			msg.rows, err = kmsgx.PlanDelete(boundsOf(d), scope, target, byTime)
			return err
		})
		return msg
	}
}

func (t *topicTab) confirmDelete(rows []kmsgx.DeleteRow) tea.Cmd {
	var total int64
	lines := make([]string, 0, len(rows)+2)
	for _, r := range rows {
		total += r.Before - r.Start
		line := fmt.Sprintf("p%d  %s → %s  (−%s)", r.Partition, groupDigits(r.Start), groupDigits(r.Before), groupDigits(r.Before-r.Start))
		if r.Clamped {
			line += "  clamped"
		}
		lines = append(lines, line)
	}
	lines = append([]string{fmt.Sprintf("Delete %s messages of %s? This can't be undone.", groupDigits(total), t.topic)}, lines...)
	cmd, why := kafkaConfirm(t.backend, t.conn, kmsgx.OpDeleteRecords, "Delete messages", lines, t.topic, delGoMsg{tabID: t.id, rows: rows})
	if why != "" {
		t.setStatus(why, true)
	}
	return cmd
}

func (t *topicTab) deleteRecords(rows []kmsgx.DeleteRow) tea.Cmd {
	t.setStatus("deleting messages…", false)
	before := make(map[int32]int64, len(rows))
	for _, r := range rows {
		before[r.Partition] = r.Before
	}
	tabCtx, id, b, conn, topic := t.ctx, t.id, t.backend, t.conn, t.topic
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, queryTimeout)
		defer cancel()
		msg := delDoneMsg{tabID: id, conn: conn}
		c, err := b.Kafka(ctx, conn)
		if err == nil {
			msg.results, err = c.DeleteRecords(ctx, topic, before)
		}
		msg.err = err
		return msg
	}
}

// deletedReport is the status of a delete: every failed partition with
// its reason.
func deletedReport(results []kafka.DeleteResult) (string, bool) {
	var failed []string
	for _, r := range results {
		if r.Err != nil {
			failed = append(failed, fmt.Sprintf("p%d: %s", r.Partition, kafkaErr(r.Err)))
		}
	}
	if len(failed) == 0 {
		return "deleted messages on " + plural(len(results), "partition"), false
	}
	return fmt.Sprintf("deleted on %d of %d partitions · %s", len(results)-len(failed), len(results), strings.Join(failed, " · ")), true
}

// partOpsUpdate handles the messages of adding partitions and deleting
// messages.
func (t *topicTab) partOpsUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case addPartsMsg:
		return t.confirmAddPartitions(m.total), true
	case addPartsGoMsg:
		return t.addPartitions(m.total), true
	case addPartsDoneMsg:
		if m.err != nil {
			t.setStatus("add partitions: "+kafkaErr(m.err), true)
			return nil, true
		}
		t.setStatus(fmt.Sprintf("%s has %d partitions", t.topic, m.total), false)
		return t.loadDetail(), true
	case delScopeMsg:
		return t.deleteTargetPrompt(m.scope), true
	case delTargetMsg:
		return t.planDelete(m.scope, m.target), true
	case delPlanMsg:
		if m.err != nil {
			t.setStatus(kafkaErr(m.err), true)
			return nil, true
		}
		return t.confirmDelete(m.rows), true
	case delGoMsg:
		return t.deleteRecords(m.rows), true
	case delDoneMsg:
		if m.err != nil {
			t.setStatus("delete messages: "+kafkaErr(m.err), true)
			return nil, true
		}
		t.setStatus(deletedReport(m.results))
		cmds := []tea.Cmd{t.loadDetail()}
		if t.msgs.loaded {
			t.msgs.gen++
			cmds = append(cmds, t.loadMsgs(0, t.msgs.pos, nil))
		}
		return tea.Batch(cmds...), true
	}
	return nil, false
}
