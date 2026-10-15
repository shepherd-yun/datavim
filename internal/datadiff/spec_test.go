package datadiff

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

func col(name string, nullable bool) db.Column {
	return db.Column{Name: name, Type: "varchar(50)", Nullable: nullable}
}

func pk(name string) db.Column { return db.Column{Name: name, Type: "bigint", PrimaryKey: true} }

func table(name string, idx []db.Index, cols ...db.Column) db.TableSchema {
	return db.TableSchema{Ref: db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: name}, Columns: cols, Indexes: idx}
}

var primary = []db.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true}}

func unique(name string, cols ...string) db.Index {
	return db.Index{Name: name, Columns: cols, Unique: true}
}

func TestNewSpec(t *testing.T) {
	tests := []struct {
		name     string
		src, tgt db.TableSchema
		want     Spec
		err      string
	}{
		{
			"same columns",
			table("a", primary, pk("id"), col("name", false)),
			table("b", primary, pk("id"), col("name", false)),
			Spec{Columns: []string{"id", "name"}, Key: []string{"id"}},
			"",
		},
		{
			"columns only one side has are left out, in the source's order",
			table("a", primary, pk("id"), col("email", true), col("name", false)),
			table("b", primary, col("name", false), pk("id"), col("legacy", true)),
			Spec{Columns: []string{"id", "name"}, Key: []string{"id"}, SourceOnly: []string{"email"}, TargetOnly: []string{"legacy"}},
			"",
		},
		{
			"generated columns are neither compared nor written",
			table("a", primary, pk("id"), db.Column{Name: "total", Type: "int", Generated: "a + b"}),
			table("b", primary, pk("id"), db.Column{Name: "total", Type: "int", Nullable: true}),
			Spec{Columns: []string{"id"}, Key: []string{"id"}},
			"",
		},
		{
			"a unique NOT NULL index stands in for the primary key",
			table("a", []db.Index{unique("a_code", "code")}, col("code", false), col("name", true)),
			table("b", []db.Index{unique("b_code", "code")}, col("code", false), col("name", true)),
			Spec{Columns: []string{"code", "name"}, Key: []string{"code"}},
			"",
		},
		{
			"the target may have the key as another of its keys",
			table("a", []db.Index{unique("a_code", "code")}, col("code", false)),
			table("b", append([]db.Index{unique("b_code", "code")}, primary...), pk("id"), col("code", false)),
			Spec{Columns: []string{"code"}, Key: []string{"code"}, TargetOnly: []string{"id"}},
			"",
		},
		{
			"no key",
			table("a", nil, col("id", false)), table("b", primary, pk("id")),
			Spec{},
			"a has no primary key or unique NOT NULL index",
		},
		{
			"a unique index over a nullable column is no key",
			table("a", []db.Index{unique("a_email", "email")}, col("email", true)),
			table("b", []db.Index{unique("b_email", "email")}, col("email", true)),
			Spec{},
			"a has no primary key or unique NOT NULL index",
		},
		{
			"a partial unique index is no key",
			table("a", []db.Index{{Name: "a_code", Columns: []string{"code"}, Unique: true, Where: "code > 0"}}, col("code", false)),
			table("b", []db.Index{unique("b_code", "code")}, col("code", false)),
			Spec{},
			"a has no primary key or unique NOT NULL index",
		},
		{
			"the target lacks the source's key",
			table("a", primary, pk("id")),
			table("b", []db.Index{unique("b_code", "code")}, col("id", false), col("code", false)),
			Spec{},
			"b has no primary key or unique NOT NULL index on (id), the key of a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewSpec(tt.src, tt.tgt)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) || !errors.Is(err, ErrNoKey) {
					t.Fatalf("err = %v, want %q wrapping ErrNoKey", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Fatalf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestQuery(t *testing.T) {
	s := Spec{Columns: []string{"id", "Odd Col"}, Key: []string{"id"}}
	my := db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"}
	pg := db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "users"}
	for _, tt := range []struct {
		d     sqlx.Dialect
		ref   db.TableRef
		where string
		want  string
	}{
		{sqlx.MySQL, my, "", "SELECT `id`, `Odd Col` FROM `app`.`users`"},
		{sqlx.Postgres, pg, "", `SELECT "id", "Odd Col" FROM "public"."users"`},
		// The condition is closed on its own line: a trailing comment cannot
		// swallow the parenthesis.
		{sqlx.MySQL, my, "id < 3 -- small", "SELECT `id`, `Odd Col` FROM `app`.`users` WHERE (id < 3 -- small\n)"},
	} {
		if got := s.Query(tt.d, tt.ref, tt.where); got != tt.want {
			t.Errorf("Query(%q) = %q, want %q", tt.where, got, tt.want)
		}
	}
}

func TestKeyIndexesAndSwap(t *testing.T) {
	s := Spec{Columns: []string{"a", "id", "b", "code"}, Key: []string{"code", "id"}, SourceOnly: []string{"x"}, TargetOnly: []string{"y"}}
	if diff := cmp.Diff([]int{3, 1}, s.KeyIndexes()); diff != "" {
		t.Fatalf("KeyIndexes (-want +got):\n%s", diff)
	}
	w := s.Swap()
	if diff := cmp.Diff(Spec{Columns: s.Columns, Key: s.Key, SourceOnly: []string{"y"}, TargetOnly: []string{"x"}}, w); diff != "" {
		t.Fatalf("Swap (-want +got):\n%s", diff)
	}
}
