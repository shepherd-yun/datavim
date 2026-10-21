package ui

import (
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// formatFact is how a Status fact reads: 87 / 500, 12.0G, 99.8%, 3d 4h.
func formatFact(f db.Fact) string {
	format := func(v float64) string {
		switch f.Kind {
		case db.FactCount:
			return groupDigits(int64(math.Round(v)))
		case db.FactBytes:
			return humanBytes(int64(v))
		case db.FactPercent:
			return strconv.FormatFloat(v, 'f', 1, 64) + "%"
		case db.FactDuration:
			return uptime(int64(v))
		case db.FactText:
		}
		return ""
	}
	if f.Kind == db.FactText {
		if f.Text == "" {
			return missing
		}
		return f.Text
	}
	s := format(f.Num)
	if f.Of > 0 {
		s += " / " + format(f.Of)
	}
	return s
}

// metricPoint is metric i of cur as drawn: a gauge as read, a counter as a
// rate per second since prev. NaN means no value: no previous sample, no
// time between them, a counter that went down (a restart or a reset), or
// metrics that no longer line up.
func metricPoint(prev *db.ServerSample, cur db.ServerSample, i int) float64 {
	if i >= len(cur.Metrics) {
		return math.NaN()
	}
	m := cur.Metrics[i]
	if !m.Counter {
		return m.Value
	}
	if prev == nil || i >= len(prev.Metrics) || prev.Metrics[i].Name != m.Name {
		return math.NaN()
	}
	dt := cur.At.Sub(prev.At).Seconds()
	if dt <= 0 || m.Value < prev.Metrics[i].Value {
		return math.NaN()
	}
	return (m.Value - prev.Metrics[i].Value) / dt
}

// metricText is a metric's value: bytes per second, or a count (with one
// decimal below 10, so 0.5 slow queries a second shows).
func metricText(m db.Metric, v float64) string {
	if m.Unit == db.UnitBytes {
		s := humanBytes(int64(v))
		if m.Counter {
			s += "/s"
		}
		return s
	}
	if v < 10 && v != math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	return groupDigits(int64(math.Round(v)))
}

// sectionBar is the server tab's first line: every section's name, the
// current one highlighted, or "‹ Locks 3/6 ›" when they do not fit.
func sectionBar(names []string, cur, width int) string {
	parts := make([]string, len(names))
	for i, n := range names {
		if i == cur {
			parts[i] = theme.Title.Render(n)
		} else {
			parts[i] = theme.Muted.Render(n)
		}
	}
	s := strings.Join(parts, theme.Muted.Render(" · "))
	if ansi.StringWidth(s) > width {
		s = theme.Muted.Render("‹ ") + theme.Title.Render(names[cur]) +
			theme.Muted.Render(" "+strconv.Itoa(cur+1)+"/"+strconv.Itoa(len(names))+" ›")
	}
	return textutil.Fit(s, width)
}
