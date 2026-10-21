package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/panes/textview"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// resultTabs is the strip over a console's results: Summary, the session's
// message log, then a tab per statement (or command) of the latest run.
type resultTabs struct {
	labels []string // one per statement
	failed []bool
	cur    int // 0 is Summary, i is statement i
}

func (r *resultTabs) set(labels []string, failed []bool) {
	r.labels, r.failed, r.cur = labels, failed, 0
}

// step is the tab d away from the current one, wrapping around.
func (r *resultTabs) step(d int) int {
	n := len(r.labels) + 1
	return ((r.cur+d)%n + n) % n
}

// view draws the strip in width cells, scrolled to keep the current tab
// in sight.
func (r *resultTabs) view(width int) string {
	names := append([]string{"Summary"}, r.labels...)
	parts := make([]string, len(names))
	for i, n := range names {
		style := theme.Muted
		switch {
		case i == r.cur:
			style = theme.ActiveTab
		case i > 0 && r.failed[i-1]:
			style = theme.Error
		}
		if i == r.cur {
			n = "[" + n + "]"
		} else {
			n = " " + n + " "
		}
		parts[i] = style.Render(textutil.Sanitize(n))
	}
	// Start as far left as still shows the current tab.
	from, used := r.cur, ansi.StringWidth(parts[r.cur])
	for from > 0 && used+ansi.StringWidth(parts[from-1])+2 <= width {
		from--
		used += ansi.StringWidth(parts[from])
	}
	s := strings.Join(parts[from:], "")
	if from > 0 {
		s = theme.Muted.Render("‹") + s
	}
	return textutil.Fit(s, width)
}

// resultLabel is a statement's tab: its number and first word.
func resultLabel(i int, text string, failed bool) string {
	label := strconv.Itoa(i + 1)
	if f := strings.Fields(text); len(f) > 0 {
		label += " " + strings.ToUpper(ansi.Truncate(f[0], 12, "…"))
	}
	if failed {
		label += " ✗"
	}
	return label
}

// messageLines is a statement tab's message: the outcome, then the
// statement, wrapped to width.
func messageLines(outcome, text string, width int) []string {
	var lines []string
	for _, l := range strings.Split(outcome, "\n") {
		lines = append(lines, textutil.Wrap(l, width)...)
	}
	lines = append(lines, "")
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		lines = append(lines, textutil.Wrap(l, width)...)
	}
	return lines
}

// msgViewAction scrolls a statement tab's message.
func msgViewAction(v *textview.Model, a keymap.Action) {
	switch a {
	case keymap.Down:
		v.Move(1)
	case keymap.Up:
		v.Move(-1)
	case keymap.Top:
		v.Top()
	case keymap.Bottom:
		v.Bottom()
	case keymap.HalfPageDown:
		v.HalfPage(1)
	case keymap.HalfPageUp:
		v.HalfPage(-1)
	}
}

// resultTabHints are the result tab keys for a console's status line.
func resultTabHints(k *keymap.Keymap, ctx keymap.Context) string {
	return joinHints(
		keyHint(k, ctx, keymap.NextResult, "result tabs"),
		keyHint(k, ctx, keymap.ResultView, "data / message"),
	)
}
