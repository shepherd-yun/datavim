// Package theme holds sqlvim's colors, styles and pane frame.
package theme

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/ui/highlight"
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

// syntax colors SQL by highlight class; Plain is left unstyled.
var syntax = map[highlight.Class]lipgloss.Style{
	highlight.Keyword:  lipgloss.NewStyle().Bold(true).Foreground(ColorFocus),
	highlight.Builtin:  lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
	highlight.String:   lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
	highlight.Number:   lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	highlight.Comment:  lipgloss.NewStyle().Italic(true).Foreground(ColorBorder),
	highlight.Operator: lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
}

// Syntax is the style of a highlight class.
func Syntax(c highlight.Class) lipgloss.Style { return syntax[c] }

// HighlightLines colors src by spans and splits it into lines. src must be
// free of control characters other than \n (see textutil.SanitizeText).
func HighlightLines(src string, spans []highlight.Span) []string {
	var lines []string
	var b strings.Builder
	for _, s := range spans {
		for i, part := range strings.Split(src[s.Start:s.End], "\n") {
			if i > 0 {
				lines = append(lines, b.String())
				b.Reset()
			}
			switch {
			case part == "":
			case s.Class == highlight.Plain:
				b.WriteString(part)
			default:
				b.WriteString(Syntax(s.Class).Render(part))
			}
		}
	}
	return append(lines, b.String())
}

var kindTags = map[string]lipgloss.Style{
	"table":    lipgloss.NewStyle().Foreground(ColorAccent),
	"view":     lipgloss.NewStyle().Foreground(lipgloss.Color("13")),
	"column":   lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
	"routine":  lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
	"schema":   lipgloss.NewStyle().Foreground(ColorFocus),
	"database": lipgloss.NewStyle().Foreground(ColorFocus).Bold(true),
}

// KindTag colors the kind tag of a search result.
func KindTag(kind string) lipgloss.Style { return kindTags[kind] }
