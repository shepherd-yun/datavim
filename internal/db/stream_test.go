package db

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestCollect(t *testing.T) {
	rs, err := Collect(func(onColumns func([]ColumnInfo), onRow func([]Value) error) error {
		onColumns([]ColumnInfo{{Name: "id"}})
		for _, v := range []string{"1", "2"} {
			if err := onRow([]Value{{Text: v}}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := &ResultSet{Columns: []ColumnInfo{{Name: "id"}}, Rows: [][]Value{{{Text: "1"}}, {{Text: "2"}}}}
	if diff := cmp.Diff(want, rs); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}

	boom := errors.New("boom")
	if _, err := Collect(func(func([]ColumnInfo), func([]Value) error) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
