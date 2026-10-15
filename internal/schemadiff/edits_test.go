package schemadiff

import (
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func TestDiffIndexEdits(t *testing.T) {
	pk := db.Index{Name: "t_pkey", Columns: []string{"id"}, Unique: true, Primary: true}
	byName := db.Index{Name: "t_name", Columns: []string{"name"}}
	uniq := db.Index{Name: "t_email", Columns: []string{"email"}, Unique: true}
	before := []db.Index{pk, byName, uniq}
	with := func(ix db.Index, f func(*db.Index)) db.Index {
		ix.Columns = append([]string(nil), ix.Columns...)
		f(&ix)
		return ix
	}
	type change struct {
		kind db.ChangeKind
		name string
		risk db.Risk
	}
	for _, tc := range []struct {
		name   string
		after  []db.Index
		origin []int
		want   []change // drops, then adds
	}{
		{"unchanged", []db.Index{pk, byName, uniq}, []int{0, 1, 2}, nil},
		{
			"renamed only",
			[]db.Index{pk, with(byName, func(i *db.Index) { i.Name = "t_full_name" }), uniq},
			[]int{0, 1, 2},
			[]change{{db.DropIndex, "t_name", 0}, {db.AddIndex, "t_full_name", 0}},
		},
		{
			"unique keeps its columns' uniqueness",
			[]db.Index{pk, byName, with(uniq, func(i *db.Index) { i.Columns = []string{"email", "id"} })},
			[]int{0, 1, 2},
			[]change{{db.DropIndex, "t_email", 0}, {db.AddIndex, "t_email", db.Lossy}},
		},
		{
			"unique made plain",
			[]db.Index{pk, byName, with(uniq, func(i *db.Index) { i.Unique = false })},
			[]int{0, 1, 2},
			[]change{{db.DropIndex, "t_email", db.Destructive}, {db.AddIndex, "t_email", 0}},
		},
		{
			"deleted",
			[]db.Index{pk, byName},
			[]int{0, 1},
			[]change{{db.DropIndex, "t_email", db.Destructive}},
		},
		{
			"new",
			[]db.Index{pk, byName, uniq, {Name: "t_qty_idx", Columns: []string{"qty"}}},
			[]int{0, 1, 2, -1},
			[]change{{db.AddIndex, "t_qty_idx", 0}},
		},
		{"edited back to the original", []db.Index{pk, with(byName, func(*db.Index) {}), uniq}, []int{0, 1, 2}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drops, adds := DiffIndexEdits(before, tc.after, tc.origin)
			var got []change
			for _, c := range append(drops, adds...) {
				name := ""
				if c.OldIndex != nil {
					name = c.OldIndex.Name
				} else {
					name = c.NewIndex.Name
				}
				got = append(got, change{c.Kind, name, c.Risk})
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %+v, want %+v", got, tc.want)
				}
			}
		})
	}
}
