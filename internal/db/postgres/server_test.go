package postgres

import (
	"testing"
	"time"
)

func TestStaleValueRereadsAfterMaxAge(t *testing.T) {
	var c staleValue
	reads := 0
	read := func() (string, error) { reads++; return "v", nil }
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	for _, at := range []time.Duration{0, 2 * time.Second, 59 * time.Second} {
		if v, err := c.get(now.Add(at), read); v != "v" || err != nil {
			t.Fatalf("get = %q, %v", v, err)
		}
	}
	if reads != 1 {
		t.Fatalf("reads within a minute = %d", reads)
	}
	if _, _ = c.get(now.Add(61*time.Second), read); reads != 2 {
		t.Fatalf("reads after a minute = %d", reads)
	}
}
