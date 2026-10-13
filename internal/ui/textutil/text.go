// Package textutil holds display-width aware string helpers for the TUI.
package textutil

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Sanitize makes database text safe to draw on one terminal line.
func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return '↵'
		case r == '\t':
			return ' '
		case r == '\r':
			return -1
		case r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
			return '�'
		}
		return r
	}, s)
}

// SanitizeText is Sanitize for multi-line text: newlines are kept.
func SanitizeText(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = Sanitize(l)
	}
	return strings.Join(lines, "\n")
}

// Fit truncates s to width cells (ending in "…") and pads it to exactly width.
func Fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(s, width, "…")
	if pad := width - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// PadLines returns s with exactly n lines.
func PadLines(s string, n int) string {
	var lines []string
	if s != "" {
		lines = strings.Split(s, "\n")
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return strings.Join(lines[:n], "\n")
}

// Wrap hard-wraps s into lines of at most width cells.
func Wrap(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var lines []string
	var cur strings.Builder
	w := 0
	for _, r := range s {
		rw := ansi.StringWidth(string(r))
		if w+rw > width && w > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
			w = 0
		}
		cur.WriteRune(r)
		w += rw
	}
	return append(lines, cur.String())
}

// Table lays out rows under headers with two-space gaps.
func Table(headers []string, rows [][]string) []string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = ansi.StringWidth(h)
	}
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], ansi.StringWidth(Sanitize(c)))
		}
	}
	line := func(cells []string) string {
		parts := make([]string, len(cells))
		for i, c := range cells {
			parts[i] = Fit(Sanitize(c), widths[i])
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ")
	}
	rule := make([]string, len(widths))
	for i, w := range widths {
		rule[i] = strings.Repeat("─", w)
	}
	out := []string{line(headers), strings.TrimRight(strings.Join(rule, "  "), " ")}
	if len(rows) == 0 {
		return append(out, "(none)")
	}
	for _, r := range rows {
		out = append(out, line(r))
	}
	return out
}

// Clamp limits v to [lo, hi]; if hi < lo it returns lo.
func Clamp(v, lo, hi int) int {
	if hi < lo || v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
