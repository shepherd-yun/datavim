// Package theme holds sqlvim's colors, styles and pane frame.
package theme

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
)

var (
	ColorBorder = lipgloss.Color("8")
	ColorFocus  = lipgloss.Color("12")
	ColorError  = lipgloss.Color("9")
	ColorAccent = lipgloss.Color("14")

	Muted       = lipgloss.NewStyle().Foreground(ColorBorder)
	Error       = lipgloss.NewStyle().Foreground(ColorError)
	Prod        = lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	ProdMode    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(ColorError)
	Accent      = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	Title       = lipgloss.NewStyle().Bold(true)
	Selected    = lipgloss.NewStyle().Reverse(true)
	SelectedDim = lipgloss.NewStyle().Underline(true)
	Mode        = lipgloss.NewStyle().Bold(true).Reverse(true)
	Null        = lipgloss.NewStyle().Foreground(ColorBorder).Italic(true)
	Header      = lipgloss.NewStyle().Bold(true)
	ActiveTab   = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
)

// Box frames body with a titled border of exactly width x height cells.
func Box(title, body string, width, height int, focused bool) string {
	return Frame(title, body, width, height, focused, false)
}

// Frame is Box with a red border when danger is set (a prod connection),
// whatever the focus.
func Frame(title, body string, width, height int, focused, danger bool) string {
	if width < 2 || height < 2 {
		return ""
	}
	inner := width - 2
	border := lipgloss.NewStyle().Foreground(ColorBorder)
	switch {
	case danger:
		border = border.Foreground(ColorError)
	case focused:
		border = border.Foreground(ColorFocus)
	}

	head := ""
	if title != "" {
		head = ansi.Truncate(" "+title+" ", inner, "…")
	}
	rest := inner - ansi.StringWidth(head)
	lines := make([]string, 0, height)
	lines = append(lines, border.Render("┌")+Title.Render(head)+border.Render(strings.Repeat("─", rest)+"┐"))

	bodyLines := strings.Split(body, "\n")
	for i := range height - 2 {
		l := ""
		if i < len(bodyLines) {
			l = bodyLines[i]
		}
		lines = append(lines, border.Render("│")+textutil.Fit(l, inner)+border.Render("│"))
	}
	lines = append(lines, border.Render("└"+strings.Repeat("─", inner)+"┘"))
	return strings.Join(lines, "\n")
}
