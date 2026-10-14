package keymap

import (
	"slices"
	"strings"
	"testing"
)

func TestParseKeys(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"gg", []string{"g", "g"}},
		{"<leader>s", []string{LeaderToken, "s"}},
		{"<leader><leader>", []string{LeaderToken, LeaderToken}},
		{"<ctrl+d>", []string{"ctrl+d"}},
		{"<shift+tab>", []string{"shift+tab"}},
		{"<space>yc", []string{"space", "y", "c"}},
		{"f<", []string{"f", "<"}},
		{"f>", []string{"f", ">"}},
		{"<", []string{"<"}},
		{"$", []string{"$"}},
		{"é", []string{"é"}},
		{"f<a", []string{"f", "<", "a"}},
		{"<a>", []string{"<", "a", ">"}},
		{"<--", []string{"<", "-", "-"}},
	} {
		got, err := ParseKeys(tc.in)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("ParseKeys(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"", "<ctrl+d", "<leader", "<ctrl+>"} {
		if _, err := ParseKeys(in); err == nil {
			t.Errorf("ParseKeys(%q) succeeded, want an error", in)
		}
	}
}

func TestParseKeysRoundTripsDisplay(t *testing.T) {
	k := Default()
	for _, b := range k.bindings {
		s := k.Display(b.Keys)
		got, err := ParseKeys(s)
		if err != nil || !slices.Equal(got, b.Keys) {
			t.Errorf("ParseKeys(Display(%q)) = %q, %v", b.Keys, got, err)
		}
	}
}

func TestParseLeader(t *testing.T) {
	for in, want := range map[string]string{"": "space", "space": "space", "<space>": "space", ",": ",", "\\": "\\"} {
		if got, err := parseLeader(in); err != nil || got != want {
			t.Errorf("parseLeader(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"ab", "<leader>", "g g"} {
		if _, err := parseLeader(in); err == nil || !strings.Contains(err.Error(), "leader") {
			t.Errorf("parseLeader(%q) = %v, want a leader error", in, err)
		}
	}
}
