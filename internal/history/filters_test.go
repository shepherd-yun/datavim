package history

import (
	"fmt"
	"slices"
	"testing"
)

func TestFilters(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []string{"a:1", "b:2", "a:1", "c:3"} {
		if err := s.AddFilter("es\x00orders", q); err != nil {
			t.Fatal(err)
		}
	}
	s.AddFilter("es\x00other", "x")
	got, err := s.Filters("es\x00orders", 10)
	if err != nil || !slices.Equal(got, []string{"c:3", "a:1", "b:2"}) {
		t.Fatalf("got %v %v", got, err)
	}
	for i := range 60 {
		s.AddFilter("k", fmt.Sprint(i))
	}
	if got, _ := s.Filters("k", 100); len(got) != MaxFilters || got[0] != "59" {
		t.Errorf("kept %d, newest %v", len(got), got[:1])
	}
}
