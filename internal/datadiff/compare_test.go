package datadiff

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

var users = Spec{Columns: []string{"id", "name"}, Key: []string{"id"}}

func v(s string) db.Value { return db.Value{Text: s} }

var null = db.Value{Null: true}

func r(vals ...db.Value) []db.Value { return vals }

// compare runs one comparison: the source's rows, then the target's.
func compare(s Spec, limit int, src, tgt [][]db.Value) ([]Row, error) {
	so := s.NewSource(limit)
	for _, row := range src {
		if err := so.Add(row); err != nil {
			return nil, err
		}
	}
	m := so.Match()
	for _, row := range tgt {
		if err := m.Add(row); err != nil {
			return nil, err
		}
	}
	return m.Rows(), nil
}

func TestCompare(t *testing.T) {
	src := [][]db.Value{r(v("1"), v("alice")), r(v("2"), v("bob")), r(v("4"), v("dave")), r(v("5"), v(""))}
	tgt := [][]db.Value{r(v("5"), null), r(v("3"), v("carol")), r(v("2"), v("robert")), r(v("1"), v("alice"))}
	got, err := compare(users, 10, src, tgt)
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{
		{Status: Changed, Source: r(v("2"), v("bob")), Target: r(v("2"), v("robert"))},
		{Status: Removed, Target: r(v("3"), v("carol"))},
		{Status: Added, Source: r(v("4"), v("dave"))},
		{Status: Changed, Source: r(v("5"), v("")), Target: r(v("5"), null)}, // '' is not NULL
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if got[0].Differs(0) || !got[0].Differs(1) || got[1].Differs(1) {
		t.Errorf("Differs: %+v", got[:2])
	}
	if a, rm, c := Count(got); a != 1 || rm != 1 || c != 2 {
		t.Errorf("Count = %d %d %d", a, rm, c)
	}
}

func TestRowsSortByKey(t *testing.T) {
	ints := [][]db.Value{r(v("10"), v("")), r(v("9"), v("")), r(v("-1"), v("")), r(v("-20"), v(""))}
	got, err := compare(users, 10, ints, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range got {
		ids = append(ids, row.Source[0].Text)
	}
	if diff := cmp.Diff([]string{"-20", "-1", "9", "10"}, ids); diff != "" {
		t.Fatalf("integers by value (-want +got):\n%s", diff)
	}

	pair := Spec{Columns: []string{"a", "b"}, Key: []string{"a", "b"}}
	got, err = compare(pair, 10, nil, [][]db.Value{r(v("1"), v("y")), r(v("1"), v("x")), r(v("0"), v("z")), r(v("1"), v("B"))})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, row := range got {
		keys = append(keys, row.Target[0].Text+row.Target[1].Text)
	}
	if diff := cmp.Diff([]string{"0z", "1B", "1x", "1y"}, keys); diff != "" {
		t.Fatalf("text byte by byte, column by column (-want +got):\n%s", diff)
	}
}

func TestRowLimit(t *testing.T) {
	two := [][]db.Value{r(v("1"), v("a")), r(v("2"), v("b"))}
	three := [][]db.Value{two[0], two[1], r(v("3"), v("c"))}
	if _, err := compare(users, 2, two, two); err != nil {
		t.Fatalf("exactly the limit: %v", err)
	}
	var tm TooManyRowsError
	if _, err := compare(users, 2, three, nil); !errors.As(err, &tm) || tm.Limit != 2 {
		t.Fatalf("source over the limit: %v", err)
	}
	if _, err := compare(users, 2, two, three); !errors.As(err, &tm) {
		t.Fatalf("target over the limit: %v", err)
	}
	if tm.Error() != "more than 2 rows (compare_max_rows)" {
		t.Errorf("Error() = %q", tm.Error())
	}
}

func TestDuplicateKey(t *testing.T) {
	dup := [][]db.Value{r(v("1"), v("a")), r(v("1"), v("b"))}
	for name, sides := range map[string][2][][]db.Value{"source": {dup, nil}, "target": {nil, dup}} {
		_, err := compare(users, 10, sides[0], sides[1])
		if !errors.Is(err, ErrDuplicateKey) || !strings.Contains(err.Error(), "(1)") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// A pass over the target that broke off (the connection was lost) is
// read again from the start with a new Matcher: nothing counts twice.
func TestMatchStartsOverEachPass(t *testing.T) {
	so := users.NewSource(10)
	for _, row := range [][]db.Value{r(v("1"), v("a")), r(v("2"), v("b"))} {
		if err := so.Add(row); err != nil {
			t.Fatal(err)
		}
	}
	broken := so.Match()
	if err := broken.Add(r(v("1"), v("a"))); err != nil {
		t.Fatal(err)
	}
	m := so.Match()
	for _, row := range [][]db.Value{r(v("1"), v("a")), r(v("2"), v("b"))} {
		if err := m.Add(row); err != nil {
			t.Fatalf("second pass: %v", err)
		}
	}
	if rows := m.Rows(); len(rows) != 0 || m.Len() != 2 {
		t.Fatalf("rows = %+v, Len = %d", rows, m.Len())
	}
}

// Drivers may hand every row in one reused slice.
func TestAddCopiesTheRow(t *testing.T) {
	so := users.NewSource(10)
	row := r(v("1"), v("a"))
	if err := so.Add(row); err != nil {
		t.Fatal(err)
	}
	row[1] = v("z")
	m := so.Match()
	tgt := r(v("1"), v("a"))
	if err := m.Add(tgt); err != nil {
		t.Fatal(err)
	}
	tgt[1] = v("q")
	if rows := m.Rows(); len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestSwapRows(t *testing.T) {
	in := []Row{
		{Status: Added, Source: r(v("4"), v("dave"))},
		{Status: Removed, Target: r(v("3"), v("carol"))},
		{Status: Changed, Source: r(v("2"), v("bob")), Target: r(v("2"), v("robert"))},
	}
	want := []Row{
		{Status: Removed, Target: r(v("4"), v("dave"))},
		{Status: Added, Source: r(v("3"), v("carol"))},
		{Status: Changed, Source: r(v("2"), v("robert")), Target: r(v("2"), v("bob"))},
	}
	if diff := cmp.Diff(want, SwapRows(in)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if in[0].Status != Added {
		t.Fatal("SwapRows changed its input")
	}
}
