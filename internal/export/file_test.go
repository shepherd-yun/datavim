package export

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shepherd-yun/datavim/internal/db"
)

func TestFileName(t *testing.T) {
	now := time.Date(2026, 10, 13, 15, 4, 5, 0, time.UTC)
	cases := map[string]string{
		"users":        "users-20261013-150405.csv",
		"app/users v2": "app_users_v2-20261013-150405.csv",
		"":             "query-20261013-150405.csv",
		"李雷":           "__-20261013-150405.csv",
	}
	for base, want := range cases {
		if got := FileName(base, CSV, now); got != want {
			t.Errorf("FileName(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.csv")
	rs := &db.ResultSet{Columns: []db.ColumnInfo{{Name: "a"}}, Rows: [][]db.Value{{{Text: "1"}}}}
	fill := func(w Writer) error { return WriteRows(w, rs) }
	if err := ToFile(path, CSV, Options{}, fill); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "a\n1\n" {
		t.Fatalf("content = %q", data)
	}
	if err := ToFile(path, CSV, Options{}, fill); err == nil {
		t.Fatal("existing files are not overwritten")
	}
	if data, _ := os.ReadFile(path); string(data) != "a\n1\n" {
		t.Fatal("existing file was modified")
	}

	broken := filepath.Join(dir, "broken.csv")
	boom := errors.New("connection reset")
	if err := ToFile(broken, CSV, Options{}, func(Writer) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(broken); !os.IsNotExist(err) {
		t.Fatal("partial file must be removed")
	}
}
