package datadiff

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
)

// Status says how a row differs.
type Status int

const (
	Added   Status = iota // only the source has the row
	Removed               // only the target has the row
	Changed               // both have it, with different values
)

// Row is one differing row. Source and Target hold the compared columns in
// Spec.Columns order; Source is nil for Removed and Target for Added.
type Row struct {
	Status Status
	Source []db.Value
	Target []db.Value
}

// Differs reports whether column i differs between the sides.
func (r Row) Differs(i int) bool { return r.Status == Changed && r.Source[i] != r.Target[i] }

// values are the row's values on the side that has it (the source first).
func (r Row) values() []db.Value {
	if r.Source != nil {
		return r.Source
	}
	return r.Target
}

// TooManyRowsError means a side has more rows than a comparison reads.
type TooManyRowsError struct{ Limit int }

func (e TooManyRowsError) Error() string {
	return fmt.Sprintf("more than %d rows (compare_max_rows)", e.Limit)
}

// ErrDuplicateKey means a side returned one key twice.
var ErrDuplicateKey = errors.New("the key is not unique")

// Source holds the source's rows by key. Add each row, then pair the
// target's rows with Match.
type Source struct {
	key   []int
	limit int
	rows  map[string][]db.Value
}

// NewSource starts reading the source; it takes at most limit rows.
func (s Spec) NewSource(limit int) *Source {
	return &Source{key: s.KeyIndexes(), limit: limit, rows: map[string][]db.Value{}}
}

// Add stores a row of the source (a copy: drivers may reuse the slice).
func (s *Source) Add(row []db.Value) error {
	if len(s.rows) >= s.limit {
		return TooManyRowsError{Limit: s.limit}
	}
	k := keyString(row, s.key)
	if _, dup := s.rows[k]; dup {
		return duplicate(row, s.key)
	}
	s.rows[k] = slices.Clone(row)
	return nil
}

// Matcher is one pass over the target's rows. A pass that broke off is
// read again with a new Matcher, so no row counts twice.
type Matcher struct {
	src   *Source
	seen  map[string]bool
	diffs []Row
}

// Match starts a pass over the target.
func (s *Source) Match() *Matcher { return &Matcher{src: s, seen: map[string]bool{}} }

// Add pairs a row of the target with the source's row of the same key.
func (m *Matcher) Add(row []db.Value) error {
	if len(m.seen) >= m.src.limit {
		return TooManyRowsError{Limit: m.src.limit}
	}
	k := keyString(row, m.src.key)
	if m.seen[k] {
		return duplicate(row, m.src.key)
	}
	m.seen[k] = true
	src, ok := m.src.rows[k]
	switch {
	case !ok:
		m.diffs = append(m.diffs, Row{Status: Removed, Target: slices.Clone(row)})
	case !slices.Equal(src, row):
		m.diffs = append(m.diffs, Row{Status: Changed, Source: src, Target: slices.Clone(row)})
	}
	return nil
}

// Len counts the target rows read.
func (m *Matcher) Len() int { return len(m.seen) }

// Rows are the differing rows, the source's unmatched rows included,
// sorted by key.
func (m *Matcher) Rows() []Row {
	out := slices.Clone(m.diffs)
	for k, row := range m.src.rows {
		if !m.seen[k] {
			out = append(out, Row{Status: Added, Source: row})
		}
	}
	key := m.src.key
	slices.SortFunc(out, func(a, b Row) int {
		av, bv := a.values(), b.values()
		for _, i := range key {
			if c := compareValues(av[i], bv[i]); c != 0 {
				return c
			}
		}
		return 0
	})
	return out
}

// Count counts the rows of each status.
func Count(rows []Row) (added, removed, changed int) {
	for _, r := range rows {
		switch r.Status {
		case Added:
			added++
		case Removed:
			removed++
		case Changed:
			changed++
		}
	}
	return added, removed, changed
}

// SwapRows turns the rows of source → target into those of target → source.
func SwapRows(rows []Row) []Row {
	out := make([]Row, len(rows))
	for i, r := range rows {
		r.Source, r.Target = r.Target, r.Source
		switch r.Status {
		case Added:
			r.Status = Removed
		case Removed:
			r.Status = Added
		}
		out[i] = r
	}
	return out
}

// keyString encodes the key's values so that different keys never encode
// alike: each value is N; for NULL, else its length, a colon and its text.
func keyString(row []db.Value, key []int) string {
	var b strings.Builder
	for _, i := range key {
		if row[i].Null {
			b.WriteString("N;")
			continue
		}
		b.WriteString(strconv.Itoa(len(row[i].Text)))
		b.WriteByte(':')
		b.WriteString(row[i].Text)
	}
	return b.String()
}

func duplicate(row []db.Value, key []int) error {
	parts := make([]string, len(key))
	for j, i := range key {
		parts[j] = row[i].Text
		if row[i].Null {
			parts[j] = "NULL"
		}
	}
	return fmt.Errorf("the key (%s) was read twice: %w", strings.Join(parts, ", "), ErrDuplicateKey)
}

var intRe = regexp.MustCompile(`^-?[0-9]+$`)

// compareValues orders key values: NULL first, integers by value, other
// text byte by byte.
func compareValues(a, b db.Value) int {
	switch {
	case a.Null && b.Null:
		return 0
	case a.Null:
		return -1
	case b.Null:
		return 1
	case intRe.MatchString(a.Text) && intRe.MatchString(b.Text):
		return compareInts(a.Text, b.Text)
	}
	return strings.Compare(a.Text, b.Text)
}

// compareInts compares decimal integers of any length.
func compareInts(a, b string) int {
	na, nb := strings.HasPrefix(a, "-"), strings.HasPrefix(b, "-")
	if na != nb {
		if na {
			return -1
		}
		return 1
	}
	a = strings.TrimLeft(strings.TrimPrefix(a, "-"), "0")
	b = strings.TrimLeft(strings.TrimPrefix(b, "-"), "0")
	c := cmp.Compare(len(a), len(b))
	if c == 0 {
		c = strings.Compare(a, b)
	}
	if na {
		return -c
	}
	return c
}
