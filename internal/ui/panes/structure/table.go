package structure

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
)

// table lays rows out under headers like textutil.Table and styles each
// padded cell with style(row, col); ok false leaves a cell plain. Cells
// are sanitized here, before styling.
func table(headers []string, rows [][]string, style func(row, col int) (lipgloss.Style, bool)) []string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = ansi.StringWidth(h)
	}
	clean := make([][]string, len(rows))
	for r, cells := range rows {
		clean[r] = make([]string, len(cells))
		for i, c := range cells {
			clean[r][i] = textutil.Sanitize(c)
			widths[i] = max(widths[i], ansi.StringWidth(clean[r][i]))
		}
	}
	line := func(cells []string, row int) string {
		parts := make([]string, len(cells))
		for i, c := range cells {
			parts[i] = textutil.Fit(c, widths[i])
			if row >= 0 && style != nil {
				if st, ok := style(row, i); ok {
					parts[i] = st.Render(parts[i])
				}
			}
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ")
	}
	rule := make([]string, len(widths))
	for i, w := range widths {
		rule[i] = strings.Repeat("─", w)
	}
	out := []string{line(headers, -1), strings.TrimRight(strings.Join(rule, "  "), " ")}
	if len(rows) == 0 {
		return append(out, "(none)")
	}
	for r, cells := range clean {
		out = append(out, line(cells, r))
	}
	return out
}
