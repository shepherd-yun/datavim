package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

// sendPreviewOver is how many messages a copy or a file send takes before
// it shows a preview and asks first (design §7.4).
const sendPreviewOver = 1000

// resendMaxBytes is the largest draft a copy makes: a bigger one would
// slow the editor and the saved draft. A var so tests can lower it.
var resendMaxBytes = 16 << 20

type (
	// resendPickMsg asks the root model for the topic to copy messages to.
	resendPickMsg struct {
		conn, topic, draft string
		count, skipped     int
		masked             int // left out: [[mask]] rules hid some of them
	}
	// resendTopicsMsg is the topics the copy may go to.
	resendTopicsMsg struct {
		resendPickMsg
		topics []string
		err    error
	}
	// resendMsg asks the root model to append draft to the producer
	// console of topic.
	resendMsg struct {
		conn, topic, draft string
		count, skipped     int
		masked             int
		confirmed          bool
	}
)

func (m resendTopicsMsg) failure() (string, error) { return m.conn, m.err }

// resend is p (only the cursor's message) or P (every message shown): the
// draft of them, then the topic to send it to.
func (t *topicTab) resend(all bool) tea.Cmd {
	v := t.msgs
	var recs []kafka.Record
	skipped, masked := 0, 0
	if all {
		for _, i := range v.shown {
			switch {
			case v.recs[i].Truncated:
				skipped++
			case v.isMasked(v.recs[i]):
				masked++
			default:
				recs = append(recs, v.recs[i])
			}
		}
	} else {
		r, ok := v.current()
		if !ok {
			return nil
		}
		if r.Truncated {
			t.setStatus(fmt.Sprintf("p%d:%d has only its first 1 MB here; it can't be sent again as it was", r.Partition, r.Offset), true)
			return nil
		}
		if v.isMasked(r) {
			t.setStatus(fmt.Sprintf("p%d:%d has masked fields; it can't be sent again as it was", r.Partition, r.Offset), true)
			return nil
		}
		recs = []kafka.Record{r}
	}
	if len(recs) == 0 {
		t.setStatus("no message to copy", true)
		return nil
	}
	draft := kmsgx.FormatDraft(recs)
	if len(draft) > resendMaxBytes {
		why := fmt.Sprintf("%s are %s as a draft (over %s): filter fewer", plural(len(recs), "message"),
			humanBytes(int64(len(draft))), humanBytes(int64(resendMaxBytes)))
		if exp := keyHint(t.keys, keymap.Global, keymap.ExportAs("jsonl"), "export them"); exp != "" {
			why += ", or " + exp + " and :send the file"
		}
		t.setStatus(why, true)
		return nil
	}
	msg := resendPickMsg{conn: t.conn, topic: t.topic, draft: draft, count: len(recs), skipped: skipped, masked: masked}
	return func() tea.Msg { return msg }
}

// resendPick reads the connection's topics for the picker.
func (m *Model) resendPick(msg resendPickMsg) tea.Cmd {
	b := m.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := resendTopicsMsg{resendPickMsg: msg}
		out.err = withKafka(ctx, b, msg.conn, func(c kafka.Client) error {
			ts, err := c.Topics(ctx)
			for _, t := range ts {
				out.topics = append(out.topics, t.Name)
			}
			return err
		})
		return out
	}
}

// resendTopics is the picker: the current topic first.
func (m *Model) resendTopics(msg resendTopicsMsg) {
	if msg.err != nil {
		m.setStatus("copy messages: "+kafkaErr(msg.err), true)
		return
	}
	names := []string{msg.topic}
	for _, t := range msg.topics {
		if t != msg.topic {
			names = append(names, t)
		}
	}
	p := msg.resendPickMsg
	m.overlay = overlay.NewPicker(fmt.Sprintf("Copy %s to the producer console of", plural(p.count, "message")), names, func(i int) tea.Msg {
		return resendMsg{conn: p.conn, topic: names[i], draft: p.draft, count: p.count, skipped: p.skipped, masked: p.masked}
	})
}

// resendTo opens (or shows) topic's producer console and appends the
// draft; nothing is sent.
func (m *Model) resendTo(msg resendMsg) tea.Cmd {
	if msg.count > sendPreviewOver && !msg.confirmed {
		ok := msg
		ok.confirmed = true
		m.overlay = overlay.NewConfirm("Copy messages", []string{
			fmt.Sprintf("Copy %s messages to produce:%s?", groupDigits(int64(msg.count)), msg.topic),
		}, "", ok)
		return nil
	}
	open := m.openProducer(msg.conn, msg.topic)
	key := producerTabKey(msg.conn, msg.topic)
	i := slices.IndexFunc(m.tabs, func(t tab) bool { return t.Key() == key })
	if i < 0 {
		return open
	}
	p := m.tabs[i].(*producerTab)
	status := fmt.Sprintf("copied %s to produce:%s · nothing is sent until you run them", plural(msg.count, "message"), msg.topic)
	if run := keyHint(m.keys, keymap.Console, keymap.RunAll, "runs them"); run != "" {
		status += " (" + run + ")"
	}
	if msg.skipped > 0 {
		status += fmt.Sprintf(" · skipped %d cut to their first 1 MB", msg.skipped)
	}
	if msg.masked > 0 {
		status += fmt.Sprintf(" · skipped %d with masked fields", msg.masked)
	}
	m.setStatus(status, msg.skipped+msg.masked > 0)
	return tea.Batch(open, p.appendDraft(msg.draft))
}

// appendDraft adds text after the draft, behind a ===, with the cursor on
// its first line; before the saved draft is read it waits for it.
func (t *producerTab) appendDraft(text string) tea.Cmd {
	if !t.draftLoaded {
		t.appendQ = append(t.appendQ, text)
		return nil
	}
	cur := strings.TrimRight(t.editor.Value(), "\n ")
	text = strings.TrimRight(text, "\n")
	first := 0
	if cur != "" {
		first = strings.Count(cur, "\n") + 2
		text = cur + "\n===\n" + text
	}
	t.editor.SetValue(text)
	t.editor.SetCursor(first, 0)
	return t.scheduleDraft()
}

// fileParsedMsg is a JSON Lines file read for :send.
type fileParsedMsg struct {
	tabID, gen int
	name       string
	recs       []kmsgx.DraftRecord
	err        error
}

func (m fileParsedMsg) TabID() int { return m.tabID }

// sendFile is :send <path>: every message of a JSON Lines export, parsed
// first, then confirmed once — even where a draft's send is not: nobody
// read the file in the editor.
func (t *producerTab) sendFile(path string) tea.Cmd {
	if t.running {
		t.setStatus("a send is already running", true)
		return nil
	}
	if cfg, _ := t.backend.Connection(t.conn); cfg.IsReadOnly() {
		t.setStatus(readOnlyHint, true)
		return nil
	}
	if strings.TrimSpace(path) == "" {
		t.setStatus(":send <file.jsonl>", true)
		return nil
	}
	t.gen++
	id, gen := t.id, t.gen
	t.setStatus("reading "+path+"…", false)
	return func() tea.Msg {
		home, _ := os.UserHomeDir()
		p := expandHome(strings.TrimSpace(path), home)
		msg := fileParsedMsg{tabID: id, gen: gen, name: filepath.Base(p)}
		f, err := os.Open(p)
		if err != nil {
			msg.err = err
			return msg
		}
		defer func() { _ = f.Close() }()
		msg.recs, msg.err = kmsgx.ParseJSONL(f)
		return msg
	}
}

func (t *producerTab) fileParsed(m fileParsedMsg) tea.Cmd {
	if m.err != nil {
		why := m.name + " " + m.err.Error()
		if !strings.HasPrefix(m.err.Error(), "line ") {
			why = m.err.Error()
		}
		t.setStatus(why, true)
		t.grid.SetResult(textResult([]string{"RESULT"}, [][]string{{"✗ " + why}}))
		t.log("✗ " + why)
		return nil
	}
	if len(m.recs) == 0 {
		t.setStatus(m.name+" has no messages", true)
		return nil
	}
	t.pending = m.recs
	lines := []string{fmt.Sprintf("Send %s messages from %s to %s?", groupDigits(int64(len(m.recs))), m.name, t.topic)}
	if len(m.recs) > sendPreviewOver {
		lines = append(lines, "The first 5:")
		for _, r := range m.recs[:5] {
			lines = append(lines, "  "+kmsgx.Preview(r.Key, r.KeyNull, 30)+" · "+kmsgx.Preview(r.Value, r.ValueNull, 60))
		}
	}
	title := "Send messages"
	if cfg, _ := t.backend.Connection(t.conn); cfg.Env == config.EnvProd {
		title = "PROD · " + title
	}
	return openOverlay(overlay.NewConfirm(title, lines, "", produceConfirmMsg{tabID: t.id, gen: t.gen, stage: stageSend}))
}
