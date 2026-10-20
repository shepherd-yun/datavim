package overlay

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// resetScope is one choice of which partitions a reset covers.
type resetScope struct {
	label string
	tps   []kafka.TP
}

// ResetOffsets is the reset-offsets dialog of a consumer group: which
// partitions, where to, a preview of every partition's move and the
// confirmation (y, or the group's name on prod).
type ResetOffsets struct {
	keys    *keymap.Keymap
	group   string
	lags    []kafka.PartitionLag
	scopes  []resetScope
	scope   int
	target  field
	focus   int // 0 scope, 1 target
	members int
	prod    bool
	now     time.Time
	submit  func([]kafka.OffsetCommit) tea.Msg
	askTime func(time.Time) tea.Cmd

	times   map[time.Time]map[kafka.TP]int64 // offsets by time, as they arrive
	rows    []kmsgx.ResetRow
	err     string
	waiting bool // the preview waits for offsets by time

	confirming bool
	name       field
	wrong      bool
}

// NewResetOffsets builds the dialog for group. sel is the partitions
// under the cursor; askTime, when the target is a time, fetches the
// offsets at it and hands them to SetTimes.
func NewResetOffsets(keys *keymap.Keymap, group string, lags []kafka.PartitionLag, sel []kafka.TP, members int, prod bool, now time.Time,
	submit func([]kafka.OffsetCommit) tea.Msg, askTime func(time.Time) tea.Cmd,
) *ResetOffsets {
	r := &ResetOffsets{keys: keys, group: group, lags: lags, members: members, prod: prod, now: now,
		submit: submit, askTime: askTime, target: newField("next"), times: map[time.Time]map[kafka.TP]int64{}}
	if len(sel) > 0 {
		label := fmt.Sprintf("%s p%d", sel[0].Topic, sel[0].Partition)
		if len(sel) > 1 {
			label = fmt.Sprintf("the %d selected partitions", len(sel))
		}
		r.scopes = append(r.scopes, resetScope{label: label, tps: sel})
		var topic []kafka.TP
		for _, l := range lags {
			if l.Topic == sel[0].Topic {
				topic = append(topic, l.TP)
			}
		}
		r.scopes = append(r.scopes, resetScope{label: fmt.Sprintf("all of %s (%d partitions)", sel[0].Topic, len(topic)), tps: topic})
	}
	var all []kafka.TP
	for _, l := range lags {
		all = append(all, l.TP)
	}
	r.scopes = append(r.scopes, resetScope{label: fmt.Sprintf("all topics (%d partitions)", len(all)), tps: all})
	r.plan()
	return r
}

// SetTimes hands over the offsets at time ts.
func (r *ResetOffsets) SetTimes(ts time.Time, byTime map[kafka.TP]int64) {
	r.times[ts] = byTime
	r.plan()
}

// plan recomputes the preview; it returns the request for offsets by
// time the target needs, if any.
func (r *ResetOffsets) plan() tea.Cmd {
	r.rows, r.err, r.waiting = nil, "", false
	t, err := kmsgx.ParseTarget(r.target.String(), r.now)
	if err != nil {
		r.err = err.Error()
		return nil
	}
	var byTime map[kafka.TP]int64
	if t.Kind == kmsgx.TargetTime {
		var ok bool
		if byTime, ok = r.times[t.Time]; !ok {
			r.waiting = true
			if r.askTime != nil {
				r.times[t.Time] = nil // asked once
				return r.askTime(t.Time)
			}
			return nil
		}
		if byTime == nil {
			r.waiting = true
			return nil
		}
	}
	r.rows, err = kmsgx.PlanReset(r.lags, r.scopes[r.scope].tps, t, byTime)
	if err != nil {
		r.err = err.Error()
	}
	return nil
}

// ready reports whether the reset can be confirmed.
func (r *ResetOffsets) ready() bool {
	return r.members == 0 && r.err == "" && !r.waiting && len(r.rows) > 0
}

func (r *ResetOffsets) commits() []kafka.OffsetCommit {
	out := make([]kafka.OffsetCommit, len(r.rows))
	for i, row := range r.rows {
		out[i] = kafka.OffsetCommit{TP: row.TP, Expect: row.From, To: row.To}
	}
	return out
}

func (r *ResetOffsets) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	key := msg.String()
	if r.confirming {
		return r.handleConfirm(msg)
	}
	switch key {
	case "esc":
		return nil, nil
	case "tab", "down", "shift+tab", "up":
		r.focus = 1 - r.focus
		return r, nil
	case "enter":
		if r.ready() {
			r.confirming, r.name, r.wrong = true, newField(""), false
		}
		return r, nil
	}
	if r.focus == 0 {
		switch key {
		case "space", "right", "l":
			r.scope = (r.scope + 1) % len(r.scopes)
		case "left", "h":
			r.scope = (r.scope + len(r.scopes) - 1) % len(r.scopes)
		}
		return r, r.plan()
	}
	r.target.handle(msg)
	return r, r.plan()
}

func (r *ResetOffsets) handleConfirm(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	key := msg.String()
	if key == "esc" {
		r.confirming = false
		return r, nil
	}
	if kmsgx.ConfirmFor(kmsgx.OpResetOffsets, r.prod) != kmsgx.ConfirmTypeName {
		switch key {
		case "y", "Y":
			return nil, emit(r.submit(r.commits()))
		case "n", "N":
			r.confirming = false
		}
		return r, nil
	}
	if key == "enter" {
		if r.name.String() == r.group {
			return nil, emit(r.submit(r.commits()))
		}
		r.wrong = true
		return r, nil
	}
	r.wrong = false
	r.name.handle(msg)
	return r, nil
}

// Paste types into the field that has the focus.
func (r *ResetOffsets) Paste(text string) {
	switch {
	case r.confirming:
		r.name.paste(text)
	case r.focus == 1:
		r.target.paste(text)
		r.plan()
	}
}

func (r *ResetOffsets) View(width, height int) string {
	w := max(min(width-4, 100), 30)
	label := func(s string, focused bool) string {
		s = fmt.Sprintf("%-8s", s)
		if focused && !r.confirming {
			return theme.ActiveTab.Render(s)
		}
		return s
	}
	lines := []string{
		label("scope", r.focus == 0) + "‹ " + r.scopes[r.scope].label + " ›",
		label("to", r.focus == 1) + r.target.View(w-12),
		theme.Muted.Render("        next · earliest · latest · an offset · @09:12 · +N / -N"),
		"",
	}
	switch {
	case r.waiting:
		lines = append(lines, "preview …")
	case r.err != "":
		for _, l := range textutil.Wrap(textutil.Sanitize(r.err), w-2) {
			lines = append(lines, theme.Error.Render(l))
		}
	default:
		for _, row := range r.rows {
			from := "-"
			if row.From >= 0 {
				from = digits(row.From)
			}
			l := fmt.Sprintf("%s p%d  %s → %s · lag %s → %s", row.Topic, row.Partition, from, digits(row.To), digits(row.LagFrom), digits(row.LagTo))
			if row.Clamped {
				l += theme.Error.Render("  clamped")
			}
			lines = append(lines, l)
		}
	}
	if r.members > 0 {
		lines = append(lines, "", theme.Error.Render(fmt.Sprintf("group has %d active members — stop the consumers first", r.members)))
	}
	lines = append(lines, "")
	switch {
	case !r.confirming:
		lines = append(lines, theme.Muted.Render("tab scope / to · ←→ scope · enter review · esc cancel"))
	case kmsgx.ConfirmFor(kmsgx.OpResetOffsets, r.prod) == kmsgx.ConfirmTypeName:
		lines = append(lines, fmt.Sprintf("Reset %d partitions? Type %s to confirm:", len(r.rows), theme.Title.Render(textutil.Sanitize(r.group))),
			"> "+r.name.View(w-4))
		if r.wrong {
			lines = append(lines, theme.Error.Render("does not match"))
		}
		lines = append(lines, theme.Muted.Render("enter confirm · esc back"))
	default:
		lines = append(lines, fmt.Sprintf("Reset %d partitions? y confirm · n back · esc back", len(r.rows)))
	}
	h := min(len(lines)+2, max(height-2, 3))
	return theme.Box("Reset offsets · group "+textutil.Sanitize(r.group), strings.Join(lines, "\n"), w, h, true)
}

// digits is n with thousands separators.
func digits(n int64) string { return kmsgx.Thousands(n) }
