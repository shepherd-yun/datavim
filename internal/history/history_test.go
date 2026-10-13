package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func TestHistoryNewestFirstWithFilter(t *testing.T) {
	s, path := open(t)
	base := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	for i, q := range []string{"select * from users", "update t set a = '50%'", "SELECT name FROM Users"} {
		e := Entry{Time: base.Add(time.Duration(i) * time.Minute), Conn: "local", Database: "app", SQL: q, Duration: 12 * time.Millisecond}
		if i == 1 {
			e.Err = "read-only"
		}
		if err := s.AddHistory(e); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.History("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].SQL != "SELECT name FROM Users" || all[2].SQL != "select * from users" {
		t.Fatalf("order: %+v", all)
	}
	if !all[2].Time.Equal(base) || all[2].Duration != 12*time.Millisecond || all[1].Err != "read-only" || all[0].Conn != "local" {
		t.Fatalf("fields not round-tripped: %+v", all)
	}
	users, _ := s.History("USERS", 10)
	if len(users) != 2 {
		t.Fatalf("case-insensitive filter: %+v", users)
	}
	pct, _ := s.History("50%", 10)
	if len(pct) != 1 {
		t.Fatalf("%% is literal: %+v", pct)
	}
	under, _ := s.History("_", 10)
	if len(under) != 0 {
		t.Fatalf("_ is literal: %+v", under)
	}
	one, _ := s.History("", 1)
	if len(one) != 1 {
		t.Fatalf("limit: %+v", one)
	}

	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state.db must be private: %v %v", info.Mode(), err)
	}
	_ = s.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	if kept, _ := again.History("", 10); len(kept) != 3 {
		t.Fatalf("history survives a restart: %d", len(kept))
	}
}

func TestDrafts(t *testing.T) {
	s, _ := open(t)
	if got, err := s.Draft("local/app"); err != nil || got != "" {
		t.Fatalf("missing draft = %q, %v", got, err)
	}
	for _, text := range []string{"select 1", "select 2"} {
		if err := s.SaveDraft("local/app", text); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.Draft("local/app"); got != "select 2" {
		t.Fatalf("draft = %q", got)
	}
	if err := s.SaveDraft("local/app", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Draft("local/app"); got != "" {
		t.Fatalf("empty text deletes the draft: %q", got)
	}
}

func TestMemoryStore(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.AddHistory(Entry{Time: time.Now(), SQL: "select 1"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.History("", 5); len(got) != 1 {
		t.Fatalf("memory store: %v", got)
	}
}
