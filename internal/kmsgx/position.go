package kmsgx

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shepherd-yun/datavim/internal/kafka"
)

// PosKind is what a start position counts from.
type PosKind int

const (
	PosLatest   PosKind = iota // the newest messages of each partition
	PosEarliest                // the start of each partition
	PosTail                    // N messages before the end of each partition
	PosOffset                  // an offset of each partition named
	PosTime                    // the first message at or after Time
)

// contextBefore is how many messages a jump to an offset shows above it.
const contextBefore = 3

// Position is a parsed start position expression.
type Position struct {
	Kind       PosKind
	Partitions []int32         // nil means all; for PosOffset the keys of Offsets
	Offsets    map[int32]int64 // PosOffset
	N          int64           // PosTail
	Time       time.Time       // PosTime
}

// Range is the offsets [From, To) of one partition.
type Range = kafka.Range

// Bounds holds each partition's start and end offset as a Range.
type Bounds map[int32]Range

// ErrOffsetGone is a jump to an offset that retention has deleted.
type ErrOffsetGone struct {
	Partition        int32
	Offset, Earliest int64
}

func (e ErrOffsetGone) Error() string {
	return fmt.Sprintf("offset %s is gone; earliest is %s", Thousands(e.Offset), Thousands(e.Earliest))
}

// Thousands formats n with comma thousands separators: 1,180,002.
func Thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// ParsePosition reads a start position: latest (or nothing), earliest,
// -N, an offset of the current partition, pN:offset (several separated by
// spaces), pN or pN,pM (alone or before latest / earliest / -N / @time),
// and @time: @2026-10-20 09:12, @09:12 (today) or @-15m. current is the
// selected partition, -1 for none; times are local.
func ParsePosition(s string, now time.Time, current int32) (Position, error) {
	s = strings.TrimSpace(s)
	fields := strings.Fields(s)
	if len(fields) > 0 && strings.Contains(fields[0], ":") && strings.HasPrefix(fields[0], "p") {
		return parseOffsets(fields)
	}
	var parts []int32
	if len(fields) > 0 && isPartitionList(fields[0]) {
		var err error
		if parts, err = parsePartitionList(fields[0]); err != nil {
			return Position{}, err
		}
		s = strings.TrimSpace(strings.TrimPrefix(s, fields[0]))
	}
	pos, err := parseStart(s, now, current, parts != nil)
	if err != nil {
		return Position{}, err
	}
	if parts != nil {
		pos.Partitions = parts
	}
	return pos, nil
}

func parseStart(s string, now time.Time, current int32, hasParts bool) (Position, error) {
	switch {
	case s == "" || s == "latest":
		return Position{Kind: PosLatest}, nil
	case s == "earliest":
		return Position{Kind: PosEarliest}, nil
	case strings.HasPrefix(s, "@"):
		t, err := parseTime(strings.TrimSpace(s[1:]), now)
		if err != nil {
			return Position{}, err
		}
		return Position{Kind: PosTime, Time: t}, nil
	case strings.HasPrefix(s, "-"):
		n, err := strconv.ParseInt(s[1:], 10, 64)
		if err != nil {
			return Position{}, fmt.Errorf("can't read %q: -N is the last N messages of each partition", s)
		}
		if n < 1 {
			return Position{}, errors.New("-N needs N ≥ 1")
		}
		return Position{Kind: PosTail, N: n}, nil
	}
	if o, err := strconv.ParseInt(s, 10, 64); err == nil && !hasParts {
		if current < 0 {
			return Position{}, fmt.Errorf("pick a partition: p1:%d", o)
		}
		return Position{Kind: PosOffset, Partitions: []int32{current}, Offsets: map[int32]int64{current: o}}, nil
	}
	return Position{}, fmt.Errorf("can't read %q: try latest, earliest, -500, p1:123, p1,p3 or @09:12", s)
}

func parseOffsets(fields []string) (Position, error) {
	pos := Position{Kind: PosOffset, Offsets: map[int32]int64{}}
	for _, f := range fields {
		p, o, ok := strings.Cut(f, ":")
		if !ok || !isPartitionList(p) || strings.Contains(p, ",") {
			return Position{}, fmt.Errorf("can't read %q: write pN:offset", f)
		}
		n, _ := strconv.ParseInt(p[1:], 10, 32) // isPartitionList checked it
		off, err := strconv.ParseInt(o, 10, 64)
		if err != nil || off < 0 {
			return Position{}, fmt.Errorf("bad offset %q in %s", o, f)
		}
		if _, dup := pos.Offsets[int32(n)]; dup {
			return Position{}, fmt.Errorf("p%d is given twice", n)
		}
		pos.Offsets[int32(n)] = off
		pos.Partitions = append(pos.Partitions, int32(n))
	}
	slices.Sort(pos.Partitions)
	return pos, nil
}

// isPartitionList reports whether s looks like p1 or p1,p3.
func isPartitionList(s string) bool {
	for p := range strings.SplitSeq(s, ",") {
		if len(p) < 2 || p[0] != 'p' {
			return false
		}
		if _, err := strconv.ParseInt(p[1:], 10, 32); err != nil {
			return false
		}
	}
	return true
}

func parsePartitionList(s string) ([]int32, error) {
	var out []int32
	for p := range strings.SplitSeq(s, ",") {
		n, err := strconv.ParseInt(p[1:], 10, 32)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("bad partition %q", p)
		}
		if !slices.Contains(out, int32(n)) {
			out = append(out, int32(n))
		}
	}
	slices.Sort(out)
	return out, nil
}

// parseTime reads 2026-10-20 09:12[:05], 09:12[:05] (today) or a negative
// duration from now (-15m, -2h).
func parseTime(s string, now time.Time) (time.Time, error) {
	if strings.HasPrefix(s, "-") {
		if d, err := time.ParseDuration(s); err == nil {
			return now.Add(d), nil
		}
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	for _, layout := range []string{"15:04:05", "15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			y, m, d := now.In(time.Local).Date()
			return time.Date(y, m, d, t.Hour(), t.Minute(), t.Second(), 0, time.Local), nil
		}
	}
	return time.Time{}, fmt.Errorf("bad time %q: try 2026-10-20 09:12, 09:12 or -15m", s)
}

// partitions are the partitions p covers: p.Partitions or all of b.
func (p Position) partitions(b Bounds) ([]int32, error) {
	if p.Partitions == nil {
		return slices.Sorted(maps.Keys(b)), nil
	}
	for _, id := range p.Partitions {
		if _, ok := b[id]; !ok {
			return nil, fmt.Errorf("partition %d doesn't exist", id)
		}
	}
	return p.Partitions, nil
}

// Ranges is what to read of each partition, at most n messages each.
// Latest reads the last n, Earliest the first n, Tail n from N before the
// end; Offset and Time start contextBefore messages before their offset
// (never before the start). byTime holds each partition's offset for
// PosTime. An offset past the end is an error, one before the start is
// ErrOffsetGone. Empty ranges are left out.
func (p Position) Ranges(b Bounds, n int64, byTime map[int32]int64) (map[int32]Range, error) {
	parts, err := p.partitions(b)
	if err != nil {
		return nil, err
	}
	out := map[int32]Range{}
	for _, id := range parts {
		bd := b[id]
		var r Range
		switch p.Kind {
		case PosLatest:
			r = Range{From: max(bd.From, bd.To-n), To: bd.To}
		case PosEarliest:
			r = Range{From: bd.From, To: min(bd.To, bd.From+n)}
		case PosTail:
			from := max(bd.From, bd.To-p.N)
			r = Range{From: from, To: min(bd.To, from+n)}
		case PosOffset, PosTime:
			o, ok := p.Offsets[id]
			if p.Kind == PosTime {
				if o, ok = byTime[id]; !ok {
					o = bd.To
				}
				o = max(o, bd.From)
			}
			if !ok && p.Kind == PosOffset {
				continue
			}
			if o > bd.To {
				return nil, fmt.Errorf("offset %s is past the end (%s)", Thousands(o), Thousands(bd.To))
			}
			if o < bd.From {
				return nil, ErrOffsetGone{Partition: id, Offset: o, Earliest: bd.From}
			}
			from := max(bd.From, o-contextBefore)
			r = Range{From: from, To: min(bd.To, from+n)}
		}
		if r.From < r.To {
			out[id] = r
		}
	}
	return out, nil
}

// Older is the next n messages before the lowest loaded offset of each
// partition; partitions already at their start are left out.
func Older(loaded map[int32]int64, b Bounds, n int64) map[int32]Range {
	out := map[int32]Range{}
	for id, low := range loaded {
		if bd, ok := b[id]; ok && low > bd.From {
			out[id] = Range{From: max(bd.From, low-n), To: low}
		}
	}
	return out
}

// Newer is the next n messages after the highest loaded offset of each
// partition; partitions already at their end are left out.
func Newer(loaded map[int32]int64, b Bounds, n int64) map[int32]Range {
	out := map[int32]Range{}
	for id, high := range loaded {
		if bd, ok := b[id]; ok && high+1 < bd.To {
			out[id] = Range{From: high + 1, To: min(bd.To, high+1+n)}
		}
	}
	return out
}

// Describe is the preview of p shown while typing it.
func (p Position) Describe(b Bounds) string {
	scope := "all partitions"
	if p.Partitions != nil {
		names := make([]string, len(p.Partitions))
		for i, id := range p.Partitions {
			names[i] = "p" + strconv.Itoa(int(id))
		}
		scope = strings.Join(names, ", ")
	}
	switch p.Kind {
	case PosEarliest:
		return "earliest · " + scope
	case PosTail:
		return fmt.Sprintf("last %s of each partition · %s", Thousands(p.N), scope)
	case PosTime:
		return "from " + p.Time.In(time.Local).Format("2006-01-02 15:04:05") + " · " + scope
	case PosOffset:
		var parts []string
		for _, id := range p.Partitions {
			s := fmt.Sprintf("p%d @ %s", id, Thousands(p.Offsets[id]))
			if bd, ok := b[id]; ok {
				s += fmt.Sprintf(" (start %s · end %s)", Thousands(bd.From), Thousands(bd.To))
			} else {
				s += " (no such partition)"
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, "; ")
	}
	return "latest · " + scope
}
