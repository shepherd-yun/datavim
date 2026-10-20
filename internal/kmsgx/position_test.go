package kmsgx

import (
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParsePosition(t *testing.T) {
	now := time.Date(2026, 10, 20, 14, 30, 0, 0, time.Local)
	for _, tc := range []struct {
		in      string
		current int32
		want    Position
		err     string
	}{
		{"", -1, Position{Kind: PosLatest}, ""},
		{"latest", -1, Position{Kind: PosLatest}, ""},
		{" earliest ", -1, Position{Kind: PosEarliest}, ""},
		{"-500", -1, Position{Kind: PosTail, N: 500}, ""},
		{"1180002", 1, Position{Kind: PosOffset, Partitions: []int32{1}, Offsets: map[int32]int64{1: 1180002}}, ""},
		{"1180002", -1, Position{}, "pick a partition: p1:1180002"},
		{"p1:5 p3:9", -1, Position{Kind: PosOffset, Partitions: []int32{1, 3}, Offsets: map[int32]int64{1: 5, 3: 9}}, ""},
		{"p1,p3", -1, Position{Kind: PosLatest, Partitions: []int32{1, 3}}, ""},
		{"p2", -1, Position{Kind: PosLatest, Partitions: []int32{2}}, ""},
		{"p2 earliest", -1, Position{Kind: PosEarliest, Partitions: []int32{2}}, ""},
		{"@2026-10-20 09:12", -1, Position{Kind: PosTime, Time: time.Date(2026, 10, 20, 9, 12, 0, 0, time.Local)}, ""},
		{"@09:12", -1, Position{Kind: PosTime, Time: time.Date(2026, 10, 20, 9, 12, 0, 0, time.Local)}, ""},
		{"@09:12:30", -1, Position{Kind: PosTime, Time: time.Date(2026, 10, 20, 9, 12, 30, 0, time.Local)}, ""},
		{"@-15m", -1, Position{Kind: PosTime, Time: now.Add(-15 * time.Minute)}, ""},
		{"p1 @-1h", -1, Position{Kind: PosTime, Partitions: []int32{1}, Time: now.Add(-time.Hour)}, ""},
		{"p9:x", -1, Position{}, `bad offset "x" in p9:x`},
		{"@yesterday", -1, Position{}, `bad time "yesterday"`},
		{"-0", -1, Position{}, "-N needs N ≥ 1"},
		{"nope", -1, Position{}, `can't read "nope"`},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParsePosition(tc.in, now, tc.current)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestPositionRanges(t *testing.T) {
	b := Bounds{0: {From: 100, To: 1000}}
	off := func(o int64) Position {
		return Position{Kind: PosOffset, Partitions: []int32{0}, Offsets: map[int32]int64{0: o}}
	}
	for _, tc := range []struct {
		name   string
		pos    Position
		byTime map[int32]int64
		want   map[int32]Range
		err    string
	}{
		{"latest", Position{Kind: PosLatest}, nil, map[int32]Range{0: {800, 1000}}, ""},
		{"earliest", Position{Kind: PosEarliest}, nil, map[int32]Range{0: {100, 300}}, ""},
		{"tail", Position{Kind: PosTail, N: 500}, nil, map[int32]Range{0: {500, 700}}, ""},
		{"tail beyond start", Position{Kind: PosTail, N: 5000}, nil, map[int32]Range{0: {100, 300}}, ""},
		{"offset", off(500), nil, map[int32]Range{0: {497, 697}}, ""},
		{"offset near start", off(101), nil, map[int32]Range{0: {100, 300}}, ""},
		{"offset at end", off(1000), nil, map[int32]Range{0: {997, 1000}}, ""},
		{"offset past end", off(1001), nil, nil, "offset 1,001 is past the end (1,000)"},
		{"offset gone", off(50), nil, nil, "offset 50 is gone; earliest is 100"},
		{"time", Position{Kind: PosTime}, map[int32]int64{0: 600}, map[int32]Range{0: {597, 797}}, ""},
		{"no such partition", Position{Kind: PosLatest, Partitions: []int32{7}}, nil, nil, "partition 7 doesn't exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.pos.Ranges(b, 200, tc.byTime)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	_, err := off(50).Ranges(b, 200, nil)
	var gone ErrOffsetGone
	if !errors.As(err, &gone) || gone != (ErrOffsetGone{Partition: 0, Offset: 50, Earliest: 100}) {
		t.Fatalf("err = %#v", err)
	}
	if e := (ErrOffsetGone{Offset: 120, Earliest: 1002113}).Error(); e != "offset 120 is gone; earliest is 1,002,113" {
		t.Fatalf("Error() = %q", e)
	}
	// several partitions, empty ones are left out
	got, err := Position{Kind: PosLatest}.Ranges(Bounds{0: {0, 50}, 1: {10, 10}, 2: {0, 500}}, 200, nil)
	if err != nil || !maps.Equal(got, map[int32]Range{0: {0, 50}, 2: {300, 500}}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestOlderNewer(t *testing.T) {
	b := Bounds{0: {From: 100, To: 1000}, 1: {From: 0, To: 50}}
	if got := Older(map[int32]int64{0: 800}, b, 200); !maps.Equal(got, map[int32]Range{0: {600, 800}}) {
		t.Fatalf("Older = %v", got)
	}
	if got := Older(map[int32]int64{0: 100}, b, 200); len(got) != 0 {
		t.Fatalf("Older at the start = %v", got)
	}
	if got := Older(map[int32]int64{0: 150, 1: 10}, b, 200); !maps.Equal(got, map[int32]Range{0: {100, 150}, 1: {0, 10}}) {
		t.Fatalf("Older = %v", got)
	}
	if got := Newer(map[int32]int64{0: 899, 1: 49}, b, 50); !maps.Equal(got, map[int32]Range{0: {900, 950}}) {
		t.Fatalf("Newer = %v", got)
	}
	if got := Newer(map[int32]int64{0: 999}, b, 50); len(got) != 0 {
		t.Fatalf("Newer at the end = %v", got)
	}
}

func TestPositionDescribe(t *testing.T) {
	b := Bounds{1: {From: 1002113, To: 1228190}, 2: {From: 0, To: 9}}
	for _, tc := range []struct {
		pos  Position
		want string
	}{
		{Position{Kind: PosOffset, Partitions: []int32{1}, Offsets: map[int32]int64{1: 1180002}}, "p1 @ 1,180,002 (start 1,002,113 · end 1,228,190)"},
		{Position{Kind: PosLatest}, "latest · all partitions"},
		{Position{Kind: PosEarliest, Partitions: []int32{1, 2}}, "earliest · p1, p2"},
		{Position{Kind: PosTail, N: 500}, "last 500 of each partition · all partitions"},
		{Position{Kind: PosTime, Time: time.Date(2026, 10, 20, 9, 12, 0, 0, time.Local)}, "from 2026-10-20 09:12:00 · all partitions"},
	} {
		if got := tc.pos.Describe(b); got != tc.want {
			t.Errorf("Describe = %q, want %q", got, tc.want)
		}
	}
}
