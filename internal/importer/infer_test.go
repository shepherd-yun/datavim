package importer

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
)

func TestInferTypes(t *testing.T) {
	tests := []struct {
		name      string
		values    []string
		mysql, pg string
	}{
		{"integers", []string{"1", "-20", "1,234"}, "bigint", "bigint"},
		{"integers written as floats", []string{"1", "2.0"}, "bigint", "bigint"},
		{"decimals", []string{"1.5", "22.25", "3"}, "decimal(4,2)", "numeric(4,2)"},
		{"exponents", []string{"1e3", "2"}, "double", "double precision"},
		{"leading zeros stay text", []string{"007", "12"}, "varchar(16)", "varchar(16)"},
		{"dates", []string{"2026-01-02", "2026/1/3"}, "date", "date"},
		{"dates and times", []string{"2026-01-02", "2026-01-02 10:00"}, "datetime", "timestamp"},
		{"fractional seconds", []string{"2026-01-02 10:00:00.5"}, "datetime(6)", "timestamp"},
		{"text", []string{"abc", "1"}, "varchar(16)", "varchar(16)"},
		{"longer text", []string{strings.Repeat("x", 40)}, "varchar(48)", "varchar(48)"},
		{"up to 255", []string{strings.Repeat("x", 250)}, "varchar(255)", "varchar(255)"},
		{"long text", []string{strings.Repeat("x", 300)}, "text", "text"},
		{"empty", []string{"", " "}, "varchar(255)", "text"},
		{"huge integers", []string{"123456789012345678901"}, "decimal(21,0)", "numeric(21,0)"},
	}
	for _, tt := range tests {
		for _, driver := range []string{"mysql", "postgres"} {
			in := NewInferrer(driver, "")
			for _, v := range tt.values {
				in.Add([]string{"k", v})
			}
			got := in.Schema(db.TableRef{Name: "t"}, []string{"k", "c"}).Columns[1].Type
			want := tt.mysql
			if driver == "postgres" {
				want = tt.pg
			}
			if got != want {
				t.Errorf("%s on %s: %s, want %s", tt.name, driver, got, want)
			}
		}
	}
}

func TestInferNullability(t *testing.T) {
	in := NewInferrer("mysql", "NULL")
	in.Add([]string{"1", "a", "x"})
	in.Add([]string{"", "", ""}) // blank rows do not count
	in.Add([]string{"2", "NULL", "y"})
	in.Add([]string{"3"}) // a short row leaves the rest empty
	s := in.Schema(db.TableRef{Name: "t"}, []string{"id", "name", "note"})
	for i, want := range []bool{false, true, true} {
		if c := s.Columns[i]; c.Nullable != want || c.Position != i+1 {
			t.Errorf("column %s: nullable %v, position %d", c.Name, c.Nullable, c.Position)
		}
	}
	if s.Columns[0].Type != "bigint" || s.Ref.Name != "t" {
		t.Fatalf("schema %+v", s)
	}
	empty := NewInferrer("postgres", "").Schema(db.TableRef{Name: "t"}, []string{"a"})
	if c := empty.Columns[0]; !c.Nullable || c.Type != "text" {
		t.Fatalf("no rows: %+v", c)
	}
}
