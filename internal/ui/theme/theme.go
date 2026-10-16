// Package theme holds sqlvim's colors, styles and pane frame.
package theme

import (
	"fmt"
	"image/color"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/ui/highlight"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
)

// defaults are the theme slots and their colors: ANSI indexes, so the
// terminal's own palette decides what they look like.
var defaults = map[string]string{
	"border": "8", "focus": "12", "error": "9", "accent": "14", "on_error": "15",
	"visual_bg": "8", "visual_fg": "15", "changed": "11", "inserted": "10", "view": "13",
	"builtin": "6", "string": "2", "number": "3", "operator": "5",
	"key_string": "2", "key_hash": "12", "key_list": "3", "key_set": "13", "key_zset": "5",
	"key_stream": "14", "key_other": "8",
}

var (
	ColorBorder, ColorFocus, ColorError, ColorAccent color.Color

	Muted, Error, Prod, ProdMode, Accent, Title, Selected, Visual, SelectedDim, Mode,
	Null, Changed, Inserted, Deleted, Header, ActiveTab lipgloss.Style

	syntax   map[highlight.Class]lipgloss.Style
	kindTags map[string]lipgloss.Style
	keyTypes map[string]lipgloss.Style
)

func init() {
	if err := Apply(nil); err != nil {
		panic(err) // defaults are valid; covered by TestApplyNilRestoresDefaults
	}
}

// Slots lists the keys config.toml's [theme] accepts.
func Slots() []string { return slices.Sorted(maps.Keys(defaults)) }

var hexColor = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

func parseColor(s string) (color.Color, bool) {
	if hexColor.MatchString(s) {
		return lipgloss.Color(s), true
	}
	if n, err := strconv.Atoi(s); err == nil && n >= 0 && n <= 255 && strconv.Itoa(n) == s {
		return lipgloss.Color(s), true
	}
	return nil, false
}

// Apply rebuilds every style from the default colors with overrides on top
// (slot name -> ANSI index "0"–"255" or "#rgb" / "#rrggbb"). Nothing changes
// when an override is invalid. Call it before the program starts.
func Apply(overrides map[string]string) error {
	c := make(map[string]color.Color, len(defaults))
	for slot, v := range defaults {
		c[slot], _ = parseColor(v)
	}
	for _, slot := range slices.Sorted(maps.Keys(overrides)) {
		v := overrides[slot]
		if _, ok := defaults[slot]; !ok {
			return fmt.Errorf("theme.%s: unknown key (want one of %s)", slot, strings.Join(Slots(), ", "))
		}
		col, ok := parseColor(v)
		if !ok {
			return fmt.Errorf("theme.%s: invalid color %q (want 0–255 or #rrggbb)", slot, v)
		}
		c[slot] = col
	}
	build(c)
	return nil
}

func build(c map[string]color.Color) {
	ColorBorder, ColorFocus, ColorError, ColorAccent = c["border"], c["focus"], c["error"], c["accent"]

	Muted = lipgloss.NewStyle().Foreground(ColorBorder)
	Error = lipgloss.NewStyle().Foreground(ColorError)
	Prod = lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	ProdMode = lipgloss.NewStyle().Bold(true).Foreground(c["on_error"]).Background(ColorError)
	Accent = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	Title = lipgloss.NewStyle().Bold(true)
	Selected = lipgloss.NewStyle().Reverse(true)
	Visual = lipgloss.NewStyle().Background(c["visual_bg"]).Foreground(c["visual_fg"])
	SelectedDim = lipgloss.NewStyle().Underline(true)
	Mode = lipgloss.NewStyle().Bold(true).Reverse(true)
	Null = lipgloss.NewStyle().Foreground(ColorBorder).Italic(true)
	Changed = lipgloss.NewStyle().Bold(true).Foreground(c["changed"])
	Inserted = lipgloss.NewStyle().Foreground(c["inserted"])
	Deleted = lipgloss.NewStyle().Foreground(ColorError).Strikethrough(true)
	Header = lipgloss.NewStyle().Bold(true)
	ActiveTab = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)

	syntax = map[highlight.Class]lipgloss.Style{
		highlight.Keyword:  lipgloss.NewStyle().Bold(true).Foreground(ColorFocus),
		highlight.Builtin:  lipgloss.NewStyle().Foreground(c["builtin"]),
		highlight.String:   lipgloss.NewStyle().Foreground(c["string"]),
		highlight.Number:   lipgloss.NewStyle().Foreground(c["number"]),
		highlight.Comment:  lipgloss.NewStyle().Italic(true).Foreground(ColorBorder),
		highlight.Operator: lipgloss.NewStyle().Foreground(c["operator"]),
	}
	keyTypes = map[string]lipgloss.Style{}
	for _, t := range []string{"string", "hash", "list", "set", "zset", "stream", "other"} {
		keyTypes[t] = lipgloss.NewStyle().Bold(true).Foreground(c["key_"+t])
	}
	kindTags = map[string]lipgloss.Style{
		"table":    lipgloss.NewStyle().Foreground(ColorAccent),
		"view":     lipgloss.NewStyle().Foreground(c["view"]),
		"column":   lipgloss.NewStyle().Foreground(c["changed"]),
		"routine":  lipgloss.NewStyle().Foreground(c["inserted"]),
		"schema":   lipgloss.NewStyle().Foreground(ColorFocus),
		"database": lipgloss.NewStyle().Foreground(ColorFocus).Bold(true),
		"console":  lipgloss.NewStyle().Foreground(c["inserted"]),
		"history":  lipgloss.NewStyle().Foreground(c["view"]),
	}
}

// Box frames body with a titled border of exactly width x height cells.
func Box(title, body string, width, height int, focused bool) string {
	return Frame(title, body, width, height, focused, false)
}

// Frame is Box with a red border when danger is set (a prod connection),
// whatever the focus; a prod frame without focus is faint red.
func Frame(title, body string, width, height int, focused, danger bool) string {
	if width < 2 || height < 2 {
		return ""
	}
	inner := width - 2
	border := lipgloss.NewStyle().Foreground(ColorBorder)
	switch {
	case danger && focused:
		border = border.Foreground(ColorError)
	case danger:
		border = border.Foreground(ColorError).Faint(true)
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

// KeyType colors a redis key's type badge; module types use key_other.
func KeyType(typ string) lipgloss.Style {
	if s, ok := keyTypes[typ]; ok && typ != "other" {
		return s
	}
	return keyTypes["other"]
}

// KindTag colors the kind tag of a search result.
func KindTag(kind string) lipgloss.Style { return kindTags[kind] }
