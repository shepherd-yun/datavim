package esx

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func summaryValue(rows [][2]string, label string) (string, bool) {
	for _, r := range rows {
		if r[0] == label {
			return r[1], true
		}
	}
	return "", false
}

func TestNodeSummary(t *testing.T) {
	got := NodeSummary(testdata(t, "node_stats_7.json"))
	var labels []string
	for _, r := range got {
		labels = append(labels, r[0])
	}
	want := []string{
		"name", "roles", "heap", "gc young / old", "cpu", "load 1m / 5m / 15m", "ram", "disk total / available",
		"open files", "docs", "store", "queries", "indexing", "rejected search / write", "uptime",
	}
	if !slices.Equal(labels, want) {
		t.Errorf("labels = %v", labels)
	}
	for label, v := range map[string]string{
		"heap": "1.2 GB / 2.0 GB (60%)", "roles": "data, ingest, master", "gc young / old": "120 / 2",
		"load 1m / 5m / 15m": "0.52 / 0.40 / 0.30", "disk total / available": "100.0 GB / 60.0 GB",
		"rejected search / write": "0 / 3", "uptime": "1d 1h 1m", "docs": "8,812,301",
	} {
		if got, _ := summaryValue(got, label); got != v {
			t.Errorf("%s = %q, want %q", label, got, v)
		}
	}

	got = NodeSummary(testdata(t, "node_stats_8.json"))
	if v, _ := summaryValue(got, "heap"); v != "1.2 GB / 2.0 GB (60%)" {
		t.Errorf("8.x heap = %q", v)
	}
	for _, missing := range []string{"load 1m / 5m / 15m", "open files", "queries", "rejected search / write"} {
		if _, ok := summaryValue(got, missing); ok {
			t.Errorf("8.x has %q", missing)
		}
	}
}

func TestExplainSummary(t *testing.T) {
	got := ExplainSummary(testdata(t, "explain_replica.json"))
	want := []string{
		"orders[0] replica unassigned",
		"cannot allocate because allocation is not permitted to any of the nodes",
		"reason: INDEX_CREATED · last allocation: no_attempt",
		"es-1: same_shard: a copy of this shard is already allocated to this node",
	}
	if !slices.Equal(got, want) {
		t.Errorf("summary =\n%s", strings.Join(got, "\n"))
	}
	if got := ExplainSummary([]byte(`oops`)); len(got) != 1 {
		t.Errorf("bad json = %v", got)
	}
}

func TestWatermark(t *testing.T) {
	const gb = 1 << 30
	for _, c := range []struct {
		v     string
		total int64
		want  float64
		ok    bool
	}{
		{"85%", 100 * gb, 85, true},
		{"0.9", 100 * gb, 90, true},
		{"50gb", 100 * gb, 50, true},
		{"500mb", 1000 << 20, 50, true},
		{"abc", 100 * gb, 0, false},
		{"50gb", 0, 0, false},
	} {
		got, ok := Watermark(c.v, c.total)
		if ok != c.ok || ok && (got < c.want-0.01 || got > c.want+0.01) {
			t.Errorf("Watermark(%q, %d) = %v, %v", c.v, c.total, got, ok)
		}
	}
}
