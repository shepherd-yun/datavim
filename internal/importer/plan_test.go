package importer

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
)

func testPlan(driver string) Plan {
	cols := []db.Column{
		{Name: "id", Type: "int"},
		{Name: "name", Type: "varchar(10)", Nullable: true},
		{Name: "n", Type: "int", Default: strp("7")},
	}
	p := Plan{Driver: driver, Table: db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "t"}, Width: 4}
	for i, c := range cols {
		src := i
		if i == 2 {
			src = 3 // the file's third column is skipped
		}
		p.Columns = append(p.Columns, Column{Source: src, Header: "h" + c.Name, Target: TargetOf(driver, c)})
	}
	return p
}

func TestPlanRow(t *testing.T) {
	p := testPlan("mysql")
	tests := []struct {
		name  string
		cells []string
		want  []Cell
		ok    bool
		err   string
	}{
		{"all columns", []string{"1", "a", "skipped", "5"}, []Cell{{Text: "1"}, {Text: "a"}, {Text: "5"}}, true, ""},
		{"short row: missing cells are empty", []string{"2"}, []Cell{{Text: "2"}, {Null: true}, {Default: true}}, true, ""},
		{"trailing empty cells are ignored", []string{"3", "b", "", "", "", " "}, []Cell{{Text: "3"}, {Text: "b"}, {Default: true}}, true, ""},
		{"blank row", []string{"", " ", ""}, nil, false, ""},
		{"extra cells with text", []string{"4", "c", "", "", "oops"}, nil, false, "line 9: 5 cells, the header has 4"},
		{"bad value", []string{"x", "c"}, nil, false, `line 9, hid: "x" is not an integer`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := p.Row(9, tt.cells)
			if tt.err != "" {
				var re *RowError
				if !errors.As(err, &re) || err.Error() != tt.err {
					t.Fatalf("err = %v, want %s", err, tt.err)
				}
				return
			}
			if err != nil || ok != tt.ok {
				t.Fatalf("ok = %v, err = %v", ok, err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Fatalf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestBatchSize(t *testing.T) {
	for cols, want := range map[int]int{0: 500, 1: 500, 131: 500, 200: 327, 70000: 1} {
		if got := BatchSize(cols); got != want {
			t.Errorf("BatchSize(%d) = %d, want %d", cols, got, want)
		}
	}
}

func TestInsert(t *testing.T) {
	rows := [][]Cell{{{Text: "1"}, {Null: true}, {Default: true}}, {{Text: "2"}, {Text: "b"}, {Text: "5"}}}
	q, args := testPlan("mysql").Insert(rows)
	if want := "INSERT INTO `app`.`t` (`id`, `name`, `n`) VALUES (?, NULL, DEFAULT), (?, ?, ?)"; q != want {
		t.Fatalf("mysql:\n%s\nwant\n%s", q, want)
	}
	if diff := cmp.Diff([]any{"1", "2", "b", "5"}, args); diff != "" {
		t.Fatalf("args (-want +got):\n%s", diff)
	}
	q, _ = testPlan("postgres").Insert(rows)
	if want := `INSERT INTO "public"."t" ("id", "name", "n") VALUES ($1, NULL, DEFAULT), ($2, $3, $4)`; q != want {
		t.Fatalf("postgres:\n%s\nwant\n%s", q, want)
	}
}

func TestNames(t *testing.T) {
	for in, want := range map[string]string{"order id": "order_id", " 订单号 ": "订单号", "a-b.c": "a_b_c", "": "fb", "%%": "fb", "_x": "_x"} {
		if got := Ident(in, "fb"); got != want {
			t.Errorf("Ident(%q) = %q, want %q", in, got, want)
		}
	}
	if MatchKey("Order ID") != MatchKey("order_id") || MatchKey("order-id") != MatchKey("ORDERID") || MatchKey("a") == MatchKey("b") {
		t.Error("MatchKey does not ignore case and separators")
	}
	got := UniqueIdents([]string{"id", "ID", "", "a b", "a_b", "a_b_2"})
	want := []string{"id", "ID_2", "column_3", "a_b", "a_b_2", "a_b_2_2"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("UniqueIdents (-want +got):\n%s", diff)
	}
}

func TestPlanRowLeavesGeneratedColumns(t *testing.T) {
	p := Plan{Driver: "mysql", Width: 1, Columns: []Column{
		{Source: 0, Header: "name", Target: TargetOf("mysql", db.Column{Name: "name", Type: "text"})},
		{Source: -1, Generate: true, Target: TargetOf("mysql", db.Column{Name: "id", Type: "int"})},
	}}
	got, ok, err := p.Row(2, []string{"a"})
	if err != nil || !ok || len(got) != 2 || got[0].Text != "a" || got[1] != (Cell{}) {
		t.Fatalf("Row = %+v, %v, %v", got, ok, err)
	}
}
