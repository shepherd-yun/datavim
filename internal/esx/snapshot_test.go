package esx

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRestoreTargets(t *testing.T) {
	got, err := RestoreTargets([]string{"logs-1", "logs-2"}, "logs-(.+)", "restored-logs-$1")
	if err != nil || !slices.Equal(got, []string{"restored-logs-1", "restored-logs-2"}) {
		t.Errorf("got %v %v", got, err)
	}
	if got, err := RestoreTargets([]string{"a", "b"}, "", "x"); err != nil || !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("empty pattern: %v %v", got, err)
	}
	if _, err := RestoreTargets([]string{"a"}, "(", "x"); err == nil {
		t.Error("bad pattern: no error")
	}
	_, err = RestoreTargets([]string{"logs-1", "logs-2"}, "logs-.*", "x")
	if err == nil || !strings.Contains(err.Error(), "logs-1 and logs-2 both restore as x") {
		t.Errorf("duplicate: %v", err)
	}
}

func TestSnapshotName(t *testing.T) {
	now := time.Date(2026, 10, 22, 23, 30, 0, 0, time.FixedZone("x", 8*3600))
	if got := SnapshotName(now); got != "snapshot-2026.10.22-153000" {
		t.Errorf("got %q", got)
	}
}
