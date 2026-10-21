package overlay

import (
	"strings"

	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// BoxOption changes a Preview or a Confirm.
type BoxOption func(*boxOptions)

type boxOptions struct {
	danger  map[int]bool
	blocked string // a Confirm that can't be confirmed, and why
}

// WithBlocked makes a Confirm one that only closes: why replaces its
// confirm key.
func WithBlocked(why string) BoxOption {
	return func(o *boxOptions) { o.blocked = why }
}

// WithDanger shows lines (0-based, before wrapping) in the error color.
func WithDanger(lines ...int) BoxOption {
	return func(o *boxOptions) {
		if o.danger == nil {
			o.danger = map[int]bool{}
		}
		for _, l := range lines {
			o.danger[l] = true
		}
	}
}

func applyBoxOptions(opts []BoxOption) boxOptions {
	var o boxOptions
	for _, f := range opts {
		f(&o)
	}
	return o
}

// wrapLines sanitizes and wraps lines to width, coloring danger lines.
func (o boxOptions) wrapLines(lines []string, width int) []string {
	var out []string
	for i, l := range lines {
		for _, part := range strings.Split(l, "\n") {
			for _, w := range textutil.Wrap(textutil.Sanitize(part), width) {
				if o.danger[i] {
					w = theme.Error.Render(w)
				}
				out = append(out, w)
			}
		}
	}
	return out
}
