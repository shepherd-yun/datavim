package kmsgx

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

// TargetKind is where a reset moves a group's committed offsets.
type TargetKind int

const (
	TargetNext     TargetKind = iota // committed + 1: skip the stuck message
	TargetEarliest                   // the partition's start
	TargetLatest                     // the partition's end
	TargetOffset                     // Offset
	TargetTime                       // the first offset at or after Time
	TargetShift                      // committed + Shift
)

// Target is a parsed reset target.
type Target struct {
	Kind          TargetKind
	Offset, Shift int64
	Time          time.Time
}

// ParseTarget reads next (or nothing), earliest, latest, an offset, +N / -N
// and @time (as in ParsePosition).
func ParseTarget(s string, now time.Time) (Target, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "" || s == "next":
		return Target{Kind: TargetNext}, nil
	case s == "earliest":
		return Target{Kind: TargetEarliest}, nil
	case s == "latest":
		return Target{Kind: TargetLatest}, nil
	case strings.HasPrefix(s, "@"):
		t, err := parseTime(strings.TrimSpace(s[1:]), now)
		return Target{Kind: TargetTime, Time: t}, err
	case strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-"):
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return Target{Kind: TargetShift, Shift: n}, nil
		}
	default:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n >= 0 {
			return Target{Kind: TargetOffset, Offset: n}, nil
		}
	}
	return Target{}, fmt.Errorf("can't read %q: try next, earliest, latest, an offset, +10, -10 or @09:12", s)
}

// ResetRow is one partition of a reset preview.
type ResetRow struct {
	kafka.TP
	From, To       int64 // From is -1 when never committed
	LagFrom, LagTo int64
	Clamped        bool // To was moved into [Start, End]
}

// PlanReset previews moving the committed offsets of the partitions in
// scope to t. byTime holds each partition's offset for TargetTime. Rows
// are sorted by topic and partition.
func PlanReset(lags []kafka.PartitionLag, scope []kafka.TP, t Target, byTime map[kafka.TP]int64) ([]ResetRow, error) {
	var rows []ResetRow
	for _, l := range lags {
		if !slices.Contains(scope, l.TP) {
			continue
		}
		var to int64
		switch t.Kind {
		case TargetNext, TargetShift:
			if l.Committed < 0 {
				return nil, fmt.Errorf("%s p%d has no committed offset; pick earliest, latest or an offset", l.Topic, l.Partition)
			}
			to = l.Committed + 1
			if t.Kind == TargetShift {
				to = l.Committed + t.Shift
			}
		case TargetEarliest:
			to = l.Start
		case TargetLatest:
			to = l.End
		case TargetOffset:
			to = t.Offset
		case TargetTime:
			o, ok := byTime[l.TP]
			if !ok {
				o = l.End
			}
			to = o
		}
		clamped := to < l.Start || to > l.End
		to = min(max(to, l.Start), l.End)
		rows = append(rows, ResetRow{TP: l.TP, From: l.Committed, To: to, LagFrom: l.Lag, LagTo: l.End - to, Clamped: clamped})
	}
	slices.SortFunc(rows, func(a, b ResetRow) int {
		return cmp.Or(strings.Compare(a.Topic, b.Topic), cmp.Compare(a.Partition, b.Partition))
	})
	return rows, nil
}
