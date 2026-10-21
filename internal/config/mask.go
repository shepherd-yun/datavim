package config

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// MaskRule hides message fields on matching connections and topics, or
// document fields on matching connections and indices (config.toml
// [[mask]]).
type MaskRule struct {
	Connections []string `toml:"connections"` // connection names
	Env         string   `toml:"env"`         // or every connection of this env ("prod")
	Topics      []string `toml:"topics"`      // path.Match globs; empty: every topic
	Indices     []string `toml:"indices"`     // Elasticsearch: path.Match globs; empty: every index
	// "key", "value", "key.<path>", "value.<path>" (Kafka) or
	// "_source.<path>" (Elasticsearch)
	Fields  []string `toml:"fields"`
	Headers []string `toml:"headers"` // header names, path.Match globs, case-insensitive
}

// ForES reports a rule for Elasticsearch connections: it names indices or
// _source paths.
func (r MaskRule) ForES() bool {
	return len(r.Indices) > 0 || slices.ContainsFunc(r.Fields, isSourceField)
}

func isSourceField(f string) bool { return strings.HasPrefix(f, "_source.") }

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
				return fmt.Errorf("%s: field %q: want key, value, key.<path>, value.<path> or _source.<path>", at, f)
			}
		}
		es := slices.ContainsFunc(r.Fields, isSourceField)
		switch {
		case es && slices.ContainsFunc(r.Fields, func(f string) bool { return !isSourceField(f) }):
			return fmt.Errorf("%s: fields mix Kafka (key / value) and Elasticsearch (_source.<path>) paths", at)
		case len(r.Indices) > 0 && !es:
			return fmt.Errorf("%s: indices go with _source.<path> fields", at)
		case es && (len(r.Topics) > 0 || len(r.Headers) > 0):
			return fmt.Errorf("%s: topics and headers are for Kafka rules, not _source.<path> fields", at)
		}
		for _, g := range r.Indices {
			if _, err := path.Match(g, ""); err != nil {
				return fmt.Errorf("%s: index %q: %w", at, g, err)
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
	if part == "_source" && !dotted {
		return false // the whole document: nothing would be left to show
	}
	if part != "key" && part != "value" && part != "_source" {
		return false
	}
	if !dotted {
		return true
	}
	// a segment is a key, key[] (each element of its array), [] or *
	return !slices.Contains(strings.Split(rest, "."), "")
}
