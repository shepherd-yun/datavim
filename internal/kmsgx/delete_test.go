package kmsgx

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseDeleteTarget(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		in   string
		want DeleteTarget
	}{
		{"1180003", DeleteTarget{Kind: DeleteBefore, Offset: 1180003}},
		{"-500", DeleteTarget{Kind: DeleteKeepLast, Keep: 500}},
		{"@09:12", DeleteTarget{Kind: DeleteBeforeTime, Time: time.Date(2026, 10, 21, 9, 12, 0, 0, time.Local)}},
		{"all", DeleteTarget{Kind: DeleteAll}},
	} {
		got, err := ParseDeleteTarget(tc.in, now)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseDeleteTarget(%q) = %+v, %v", tc.in, got, err)
		}
	}
	_, err := ParseDeleteTarget("abc", now)
	if err == nil || !strings.Contains(err.Error(), "an offset, -N, @time or all") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanDelete(t *testing.T) {
	b := Bounds{0: {From: 100, To: 1000}, 1: {From: 0, To: 300}, 2: {From: 50, To: 50}}
	for _, tc := range []struct {
		name   string
		scope  []int32
		t      DeleteTarget
		byTime map[int32]int64
		want   []DeleteRow
		err    string
	}{
		{
			"offset on one partition",
			[]int32{0},
			DeleteTarget{Kind: DeleteBefore, Offset: 400},
			nil,
			[]DeleteRow{{Partition: 0, Start: 100, Before: 400, End: 1000}},
			"",
		},
		{
			"offset on two",
			[]int32{0, 1},
			DeleteTarget{Kind: DeleteBefore, Offset: 4},
			nil, nil,
			"an offset names one partition: pick one, or use -N, @time or all",
		},
		{
			"offset past the end is clamped",
			[]int32{1},
			DeleteTarget{Kind: DeleteBefore, Offset: 9999},
			nil,
			[]DeleteRow{{Partition: 1, Start: 0, Before: 300, End: 300, Clamped: true}},
			"",
		},
		{
			"keep the newest 500",
			[]int32{0, 1, 2},
			DeleteTarget{Kind: DeleteKeepLast, Keep: 500},
			nil,
			[]DeleteRow{{Partition: 0, Start: 100, Before: 500, End: 1000}},
			"",
		},
		{
			"all",
			[]int32{0, 1, 2},
			DeleteTarget{Kind: DeleteAll},
			nil,
			[]DeleteRow{{Partition: 0, Start: 100, Before: 1000, End: 1000}, {Partition: 1, Start: 0, Before: 300, End: 300}},
			"",
		},
		{
			"by time",
			[]int32{0, 1},
			DeleteTarget{Kind: DeleteBeforeTime},
			map[int32]int64{0: 700, 1: 0},
			[]DeleteRow{{Partition: 0, Start: 100, Before: 700, End: 1000}},
			"",
		},
		{"nothing", []int32{2}, DeleteTarget{Kind: DeleteAll}, nil, nil, "nothing to delete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PlanDelete(b, tc.scope, tc.t, tc.byTime)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
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
