package schemadiff

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func TestDiffNamespace(t *testing.T) {
	id := []db.Column{{Name: "id", Type: "int"}}
	target := []db.TableSchema{table("users", id), table("tmp", id), table("same", id)}
	source := []db.TableSchema{
		table("users", []db.Column{{Name: "id", Type: "int"}, {Name: "name", Type: "text", Nullable: true}}),
		table("audit", id),
		table("same", id),
	}
	objs, identical := DiffNamespace(target, source)
	var got []string
	for _, o := range objs {
		got = append(got, DescribeObject(o))
	}
	want := []string{"+ audit  (new table, 1 column)", "- tmp", "~ users"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if identical != 1 {
		t.Errorf("identical = %d", identical)
	}
	if objs[0].Source != &source[1] || objs[1].Target != &target[1] || len(objs[2].Changes) != 1 {
		t.Errorf("objects = %+v", objs)
	}
}
