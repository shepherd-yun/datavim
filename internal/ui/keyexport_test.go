package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
)

func TestExportCells(t *testing.T) {
	rs := &db.ResultSet{
		Columns: []db.ColumnInfo{{Name: "a"}, {Name: "b"}},
		Rows: [][]db.Value{
			{{Text: "a"}, {Text: "\xff\x00"}},
			{{Text: "x\ty"}, {Text: "ok"}},
			{{Null: true}, {Text: "\x01"}},
		},
	}
	out, n := exportCells(rs)
	if n != 2 {
		t.Fatalf("n = %d", n)
	}
	if out.Rows[0][1].Text != `\xFF\x00` || out.Rows[1][0].Text != "x\ty" || out.Rows[0][0].Text != "a" || !out.Rows[2][0].Null || out.Rows[2][1].Text != `\x01` {
		t.Fatalf("rows = %v", out.Rows)
	}
	if rs.Rows[0][1].Text != "\xff\x00" {
		t.Fatal("exportCells changed its input")
	}
}

// exportKey opens key with value v and points the export directory at a
// temp dir.
func exportKey(t *testing.T, key string, v fakeValue, cfg config.Connection) (*Model, *fakeRedis, string) {
	t.Helper()
	m, _, r := newRedisModel(t, cfg)
	dir := t.TempDir()
	m.settings.ExportDir = dir
	r.put(0, key, v)
	drainAll(t, m, m.openKey("cache", 0, key))
	return m, r, dir
}

func TestKeyExportString(t *testing.T) {
	m, _, dir := exportKey(t, "greeting", fakeValue{typ: "string", str: "hello"}, config.Connection{})
	pressAll(t, m, "space", "x", "j")
	if s := screen(m); !strings.Contains(s, "Save to:") {
		t.Fatalf("no path prompt (a scope choice?):\n%s", s)
	}
	pressAll(t, m, "enter")
	if got := exportedFile(t, filepath.Join(dir, "greeting-*.json")); got != "[\n  {\"value\": \"hello\"}\n]\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.Contains(m.status, "exported 1 rows") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestKeyExportStringTruncated(t *testing.T) {
	m, _, _ := exportKey(t, "big", fakeValue{typ: "string", str: strings.Repeat("x", maxStringRead+1)}, config.Connection{})
	pressAll(t, m, "space", "x", "c", "enter")
	if !strings.Contains(m.status, " · only the first 1 MB was loaded") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestKeyExportHashWhole(t *testing.T) {
	m, _, dir := exportKey(t, "h", bigHash(5), config.Connection{})
	pressAll(t, m, "space", "x", "c")
	if s := screen(m); !strings.Contains(s, "Loaded rows (2)") || !strings.Contains(s, "Whole key (5 fields)") {
		t.Fatalf("scope choice:\n%s", s)
	}
	pressAll(t, m, "j", "enter", "enter")
	got := exportedFile(t, filepath.Join(dir, "h-*.csv"))
	if got != "field,value\nf0,v0\nf1,v1\nf2,v2\nf3,v3\nf4,v4\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.Contains(m.status, "exported 5 rows") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestKeyExportWholeDedup(t *testing.T) {
	v := bigHash(5)
	v.hash = append(v.hash[:3], v.hash[0], v.hash[3], v.hash[4]) // HSCAN returns f0 again
	m, _, dir := exportKey(t, "h", v, config.Connection{})
	pressAll(t, m, "space", "x", "c", "j", "enter", "enter")
	if got := exportedFile(t, filepath.Join(dir, "h-*.csv")); strings.Count(got, "\n") != 6 || strings.Count(got, "f0,") != 1 {
		t.Fatalf("file = %q", got)
	}
}

func TestKeyExportWholeProdPauses(t *testing.T) {
	v := fakeValue{typ: "list"}
	for range 2500 {
		v.list = append(v.list, "x")
	}
	m, r, dir := exportKey(t, "l", v, config.Connection{Env: config.EnvProd})
	pressAll(t, m, "space", "x", "c")
	if s := screen(m); !strings.Contains(s, "Whole key (2,500 elements)") {
		t.Fatalf("scope choice:\n%s", s)
	}
	pressAll(t, m, "j", "enter", "enter")
	if got := exportedFile(t, filepath.Join(dir, "l-*.csv")); strings.Count(got, "\n") != 2501 {
		t.Fatalf("lines = %d", strings.Count(got, "\n"))
	}
	n := 0
	for _, c := range r.keyCalls() {
		if c == "ListPage l" {
			n++
		}
	}
	if n < 4 { // the first page and three of 1,000
		t.Fatalf("ListPage calls = %d", n)
	}
}

func TestKeyExportLoaded(t *testing.T) {
	m, _, dir := exportKey(t, "h", bigHash(5), config.Connection{})
	pressAll(t, m, "space", "x", "c", "enter", "enter")
	if got := exportedFile(t, filepath.Join(dir, "h-*.csv")); got != "field,value\nf0,v0\nf1,v1\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestKeyExportUnsaved(t *testing.T) {
	m, _, dir := exportKey(t, "h", bigHash(2), config.Connection{})
	pressAll(t, m, "l")
	editTo(t, m, "new")
	pressAll(t, m, "space", "x", "c", "enter")
	if got := exportedFile(t, filepath.Join(dir, "h-*.csv")); got != "field,value\nf0,new\nf1,v1\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.Contains(m.status, " · includes unsaved changes") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestKeyExportBinary(t *testing.T) {
	m, _, dir := exportKey(t, "s", fakeValue{typ: "set", set: []string{"\xff"}}, config.Connection{})
	pressAll(t, m, "space", "x", "c", "enter")
	if got := exportedFile(t, filepath.Join(dir, "s-*.csv")); got != "member\n\\xFF\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.HasPrefix(m.status, "exported 1 rows") || !strings.Contains(m.status, `1 binary values written escaped (\xNN)`) {
		t.Fatalf("status = %q", m.status)
	}
}

func TestKeyExportNothing(t *testing.T) {
	m, _, _ := exportKey(t, "j", fakeValue{typ: "ReJSON-RL"}, config.Connection{})
	pressAll(t, m, "space", "x", "c")
	if m.status != "nothing to export" {
		t.Fatalf("status = %q", m.status)
	}
}
