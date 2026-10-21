package kmsgx

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DeleteKind is where a delete of messages stops.
type DeleteKind int

const (
	DeleteBefore     DeleteKind = iota // before Offset (one partition)
	DeleteKeepLast                     // keep the newest Keep of each partition
	DeleteBeforeTime                   // before the first message at or after Time
	DeleteAll
)

// DeleteTarget is a parsed delete target.
type DeleteTarget struct {
	Kind   DeleteKind
	Offset int64
	Keep   int64
	Time   time.Time
}

// ParseDeleteTarget reads an offset, -N (keep the newest N), @time and all.
func ParseDeleteTarget(s string, now time.Time) (DeleteTarget, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "all":
		return DeleteTarget{Kind: DeleteAll}, nil
	case strings.HasPrefix(s, "@"):
		t, err := parseTime(strings.TrimSpace(s[1:]), now)
		return DeleteTarget{Kind: DeleteBeforeTime, Time: t}, err
	case strings.HasPrefix(s, "-"):
		if n, err := strconv.ParseInt(s[1:], 10, 64); err == nil && n >= 0 {
			return DeleteTarget{Kind: DeleteKeepLast, Keep: n}, nil
		}
	default:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n >= 0 {
			return DeleteTarget{Kind: DeleteBefore, Offset: n}, nil
		}
	}
	return DeleteTarget{}, fmt.Errorf("can't read %q: try an offset, -N, @time or all (1180003, -500, @09:12, all)", s)
}

// DeleteRow is one partition of a delete preview.
type DeleteRow struct {
	Partition          int32
	Start, Before, End int64
	Clamped            bool // Before was moved into [Start, End]
}

// PlanDelete previews t on the partitions in scope; partitions with
// nothing to delete are left out, and none left is an error. byTime is
// OffsetsAt(t.Time) for DeleteBeforeTime.
func PlanDelete(b Bounds, scope []int32, t DeleteTarget, byTime map[int32]int64) ([]DeleteRow, error) {
	if t.Kind == DeleteBefore && len(scope) != 1 {
		return nil, errors.New("an offset names one partition: pick one, or use -N, @time or all")
	}
	scope = slices.Sorted(slices.Values(scope))
	var out []DeleteRow
	for _, p := range scope {
		bd, ok := b[p]
		if !ok {
			return nil, fmt.Errorf("partition %d doesn't exist", p)
		}
		row := DeleteRow{Partition: p, Start: bd.From, End: bd.To}
		var before int64
		switch t.Kind {
		case DeleteBefore:
			before = t.Offset
		case DeleteKeepLast:
			before = bd.To - t.Keep
		case DeleteBeforeTime:
			before = byTime[p]
		case DeleteAll:
			before = bd.To
		}
		if before < bd.From || before > bd.To {
			if t.Kind == DeleteBefore {
				row.Clamped = true
			}
			before = min(max(before, bd.From), bd.To)
		}
		row.Before = before
		if row.Before > row.Start {
			out = append(out, row)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("nothing to delete")
	}
	return out, nil
}
