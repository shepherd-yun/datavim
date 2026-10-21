package config

import (
	"slices"
	"strings"
	"testing"
)

const maskExample = `
[[mask]]
env = "prod"
topics = ["orders", "payments.*"]
fields = ["value.customer.phone", "value.cards[].number", "value.*.email", "key.ssn"]
headers = ["authorization", "x-user-*"]

[[mask]]
connections = ["events"]
fields = ["value"]

[[mask]]
connections = ["es"]
indices = ["users*"]
fields = ["_source.password", "_source.cards[].no"]
`

func TestLoadMasks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.toml", maskExample)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Masks) != 3 {
		t.Fatalf("masks = %+v", cfg.Masks)
	}
	m := cfg.Masks[0]
	if m.Env != "prod" || !slices.Equal(m.Topics, []string{"orders", "payments.*"}) || len(m.Fields) != 4 ||
		!slices.Equal(m.Headers, []string{"authorization", "x-user-*"}) || m.Connections != nil {
		t.Fatalf("mask 1 = %+v", m)
	}
	if !slices.Equal(cfg.Masks[1].Connections, []string{"events"}) || cfg.Masks[1].Fields[0] != "value" {
		t.Fatalf("mask 2 = %+v", cfg.Masks[1])
	}
	if e := cfg.Masks[2]; !slices.Equal(e.Indices, []string{"users*"}) || len(e.Fields) != 2 || !e.ForES() || m.ForES() {
		t.Fatalf("mask 3 = %+v", e)
	}
}

func TestLoadMasksInvalid(t *testing.T) {
	for _, tc := range []struct{ rule, want string }{
		{`fields = ["value"]`, "mask rule 1: set connections or env"},
		{`env = "prod"`, "mask rule 1: nothing to mask"},
		{`env = "prod"` + "\n" + `fields = ["body.x"]`, `mask rule 1: field "body.x": want key, value, key.<path>, value.<path> or _source.<path>`},
		{`env = "prod"` + "\n" + `fields = ["value."]`, `field "value."`},
		{`env = "prod"` + "\n" + `fields = ["value"]` + "\n" + `topics = ["a["]`, `topic "a["`},
		{`env = "prod"` + "\n" + `headers = ["["]`, `header "["`},
		{`env = "prod"` + "\n" + `fields = ["value.a..b"]`, `field "value.a..b"`},
		{`env = "prod"` + "\n" + `fields = ["value.x", "_source.y"]`, "fields mix Kafka (key / value) and Elasticsearch (_source.<path>) paths"},
		{`env = "prod"` + "\n" + `indices = ["a*"]` + "\n" + `fields = ["value.x"]`, "indices go with _source.<path> fields"},
		{`env = "prod"` + "\n" + `topics = ["t"]` + "\n" + `fields = ["_source.x"]`, "topics and headers are for Kafka rules, not _source.<path> fields"},
		{`env = "prod"` + "\n" + `headers = ["h"]` + "\n" + `fields = ["_source.x"]`, "topics and headers are for Kafka rules"},
		{`env = "prod"` + "\n" + `fields = ["_source"]`, `field "_source": want key, value, key.<path>, value.<path> or _source.<path>`},
		{`env = "prod"` + "\n" + `fields = ["_source.a..b"]`, `field "_source.a..b"`},
		{`env = "prod"` + "\n" + `fields = ["_source.a"]` + "\n" + `indices = ["a["]`, `index "a["`},
	} {
		dir := t.TempDir()
		writeFile(t, dir, "config.toml", "[[mask]]\n"+tc.rule+"\n")
		_, err := Load(dir)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "config.toml") {
			t.Errorf("%q: err = %v, want %q", tc.rule, err, tc.want)
		}
	}
}
