package config

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// MaskRule hides message fields on matching connections and topics
// (config.toml [[mask]]).
type MaskRule struct {
	Connections []string `toml:"connections"` // connection names
	Env         string   `toml:"env"`         // or every connection of this env ("prod")
	Topics      []string `toml:"topics"`      // path.Match globs; empty: every topic
	Fields      []string `toml:"fields"`      // "key", "value", "key.<path>", "value.<path>"
	Headers     []string `toml:"headers"`     // header names, path.Match globs, case-insensitive
}

// checkMasks validates the rules; a connection that does not exist is
// not an error (workspaces have different connections).
func checkMasks(rules []MaskRule) error {
	for i, r := range rules {
		at := fmt.Sprintf("mask rule %d", i+1)
		if len(r.Connections) == 0 && r.Env == "" {
			return fmt.Errorf("%s: set connections or env", at)
		}
		if len(r.Fields) == 0 && len(r.Headers) == 0 {
			return fmt.Errorf("%s: nothing to mask (set fields or headers)", at)
		}
		for _, f := range r.Fields {
			if !validMaskField(f) {
				return fmt.Errorf("%s: field %q: want key, value, key.<path> or value.<path>", at, f)
			}
		}
		for _, g := range r.Topics {
			if _, err := path.Match(g, ""); err != nil {
				return fmt.Errorf("%s: topic %q: %w", at, g, err)
			}
		}
		for _, g := range r.Headers {
			if _, err := path.Match(g, ""); err != nil {
				return fmt.Errorf("%s: header %q: %w", at, g, err)
			}
		}
	}
	return nil
}

func validMaskField(f string) bool {
	part, rest, dotted := strings.Cut(f, ".")
	if part != "key" && part != "value" {
		return false
	}
	if !dotted {
		return true
	}
	// a segment is a key, key[] (each element of its array), [] or *
	return !slices.Contains(strings.Split(rest, "."), "")
}
