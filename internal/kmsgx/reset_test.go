package kmsgx

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

func TestPlanReset(t *testing.T) {
	p0 := kafka.TP{Topic: "orders", Partition: 0}
	p1 := kafka.TP{Topic: "orders", Partition: 1}
	lags := []kafka.PartitionLag{
		{TP: p1, Committed: 1180002, Start: 1002113, End: 1228190, Lag: 48188},
		{TP: p0, Committed: 50, Start: 0, End: 60, Lag: 10},
	}
	row := func(to, lagTo int64, clamped bool) []ResetRow {
		return []ResetRow{{TP: p1, From: 1180002, To: to, LagFrom: 48188, LagTo: lagTo, Clamped: clamped}}
	}
	scope := []kafka.TP{p1}
	for _, tc := range []struct {
		name   string
		target Target
		byTime map[kafka.TP]int64
		want   []ResetRow
	}{
		{"next", Target{Kind: TargetNext}, nil, row(1180003, 48187, false)},
		{"shift back", Target{Kind: TargetShift, Shift: -10}, nil, row(1179992, 48198, false)},
		{"offset past end", Target{Kind: TargetOffset, Offset: 2000000}, nil, row(1228190, 0, true)},
		{"offset before start", Target{Kind: TargetOffset, Offset: 5}, nil, row(1002113, 226077, true)},
		{"earliest", Target{Kind: TargetEarliest}, nil, row(1002113, 226077, false)},
		{"latest", Target{Kind: TargetLatest}, nil, row(1228190, 0, false)},
		{"time", Target{Kind: TargetTime}, map[kafka.TP]int64{p1: 1200000}, row(1200000, 28190, false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PlanReset(lags, scope, tc.target, tc.byTime)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
	got, err := PlanReset(lags, []kafka.TP{p1, p0}, Target{Kind: TargetLatest}, nil)
	if err != nil || len(got) != 2 || got[0].TP != p0 || got[1].TP != p1 {
		t.Fatalf("rows are sorted by topic and partition: %+v, %v", got, err)
	}
	never := []kafka.PartitionLag{{TP: p1, Committed: -1, Start: 10, End: 20, Lag: 10}}
	if _, err := PlanReset(never, scope, Target{Kind: TargetNext}, nil); err == nil || !strings.Contains(err.Error(), "p1 has no committed offset") {
		t.Fatalf("err = %v", err)
	}
	got, err = PlanReset(never, scope, Target{Kind: TargetEarliest}, nil)
	if err != nil || got[0].From != -1 || got[0].To != 10 || got[0].LagTo != 10 {
		t.Fatalf("never committed to earliest: %+v, %v", got, err)
	}
}

func TestParseTarget(t *testing.T) {
	now := time.Date(2026, 10, 20, 14, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		in   string
		want Target
		err  string
	}{
		{"next", Target{Kind: TargetNext}, ""},
		{"", Target{Kind: TargetNext}, ""},
		{"earliest", Target{Kind: TargetEarliest}, ""},
		{"latest", Target{Kind: TargetLatest}, ""},
		{"1180003", Target{Kind: TargetOffset, Offset: 1180003}, ""},
		{"+10", Target{Kind: TargetShift, Shift: 10}, ""},
		{"-10", Target{Kind: TargetShift, Shift: -10}, ""},
		{"@09:12", Target{Kind: TargetTime, Time: time.Date(2026, 10, 20, 9, 12, 0, 0, time.Local)}, ""},
		{"abc", Target{}, `can't read "abc"`},
	} {
		got, err := ParseTarget(tc.in, now)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("ParseTarget(%q) err = %v", tc.in, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseTarget(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
}
