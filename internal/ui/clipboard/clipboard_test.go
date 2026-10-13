package clipboard

import (
	"errors"
	"testing"
)

func TestUseMacTool(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/tool", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		goos string
		env  map[string]string
		look func(string) (string, error)
		want bool
	}{
		{"darwin", nil, found, true},
		{"darwin", map[string]string{"SSH_TTY": "/dev/ttys001"}, found, false},
		{"darwin", nil, missing, false},
		{"linux", nil, found, false},
	}
	for _, c := range cases {
		for _, tool := range []string{"pbcopy", "pbpaste"} {
			if got := useMacTool(tool, c.goos, env(c.env), c.look); got != c.want {
				t.Errorf("useMacTool(%s, %s, %v) = %v", tool, c.goos, c.env, got)
			}
		}
	}
}
