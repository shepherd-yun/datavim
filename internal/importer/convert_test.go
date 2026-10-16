package importer

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func strp(s string) *string { return &s }

func TestTargetOf(t *testing.T) {
	tests := []struct {
		driver string
		col    db.Column
		want   Target
	}{
		{"mysql", db.Column{Name: "a", Type: "bigint unsigned"}, Target{Kind: KindInteger, Bits: 64, Unsigned: true}},
		{"mysql", db.Column{Name: "a", Type: "int(11)", Nullable: true}, Target{Kind: KindInteger, Bits: 32, Nullable: true}},
		{"mysql", db.Column{Name: "a", Type: "tinyint(1)"}, Target{Kind: KindBool}},
		{"mysql", db.Column{Name: "a", Type: "tinyint"}, Target{Kind: KindInteger, Bits: 8}},
		{"mysql", db.Column{Name: "a", Type: "mediumint", AutoIncrement: true}, Target{Kind: KindInteger, Bits: 24, Default: true}},
		{"postgres", db.Column{Name: "a", Type: "smallint"}, Target{Kind: KindInteger, Bits: 16}},
		{"postgres", db.Column{Name: "a", Type: "integer", Default: strp("0")}, Target{Kind: KindInteger, Bits: 32, Default: true}},
		{"mysql", db.Column{Name: "a", Type: "decimal(10,2)"}, Target{Kind: KindDecimal, Precision: 10, Scale: 2}},
		{"postgres", db.Column{Name: "a", Type: "numeric"}, Target{Kind: KindDecimal}},
		{"postgres", db.Column{Name: "a", Type: "double precision"}, Target{Kind: KindFloat}},
		{"postgres", db.Column{Name: "a", Type: "boolean"}, Target{Kind: KindBool}},
		{"mysql", db.Column{Name: "a", Type: "date"}, Target{Kind: KindDate}},
		{"mysql", db.Column{Name: "a", Type: "datetime(3)"}, Target{Kind: KindDateTime, Precision: 3}},
		{"postgres", db.Column{Name: "a", Type: "timestamp(3) with time zone"}, Target{Kind: KindDateTime, Precision: 3, TZ: true}},
		{"postgres", db.Column{Name: "a", Type: "timestamptz"}, Target{Kind: KindDateTime, TZ: true}},
		{"postgres", db.Column{Name: "a", Type: "time without time zone"}, Target{Kind: KindTime}},
		{"postgres", db.Column{Name: "a", Type: "timetz"}, Target{Kind: KindOther}},
		{"postgres", db.Column{Name: "a", Type: "character varying(64)"}, Target{Kind: KindString, Precision: 64, Length: 64}},
		{"mysql", db.Column{Name: "a", Type: "varchar(20)"}, Target{Kind: KindString, Precision: 20, Length: 20}},
		{"mysql", db.Column{Name: "a", Type: "enum('a','b')"}, Target{Kind: KindString}},
		{"mysql", db.Column{Name: "a", Type: "longtext"}, Target{Kind: KindString}},
		{"postgres", db.Column{Name: "a", Type: "jsonb"}, Target{Kind: KindOther}},
		{"postgres", db.Column{Name: "a", Type: "int[]"}, Target{Kind: KindOther}},
	}
	for _, tt := range tests {
		got := TargetOf(tt.driver, tt.col)
		tt.want.Name, tt.want.Type, tt.want.MySQL = tt.col.Name, tt.col.Type, tt.driver == "mysql"
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("TargetOf(%s, %q) (-want +got):\n%s", tt.driver, tt.col.Type, diff)
		}
	}
}

func TestConvert(t *testing.T) {
	col := func(driver, typ string, nullable bool) Target {
		return TargetOf(driver, db.Column{Name: "c", Type: typ, Nullable: nullable})
	}
	withDefault := TargetOf("mysql", db.Column{Name: "c", Type: "int", Default: strp("7")})
	tests := []struct {
		target Target
		in     string
		null   string
		want   Cell
		err    string // substring; "" means no error
	}{
		{col("mysql", "int", true), " 42 ", "", Cell{Text: "42"}, ""},
		{col("mysql", "int", true), "1,234", "", Cell{Text: "1234"}, ""},
		{col("mysql", "int", true), "3.0", "", Cell{Text: "3"}, ""},
		{col("mysql", "int", true), "3.5", "", Cell{}, "is not an integer"},
		{col("mysql", "int", true), "abc", "", Cell{}, "is not an integer"},
		{col("mysql", "int", true), "1.23456789012346E+17", "", Cell{}, "format the column as text"},
		{col("mysql", "tinyint", true), "128", "", Cell{}, "is out of range for tinyint"},
		{col("mysql", "int unsigned", true), "-1", "", Cell{}, "is out of range for int unsigned"},
		{col("mysql", "bigint unsigned", true), "18446744073709551615", "", Cell{Text: "18446744073709551615"}, ""},
		{col("mysql", "decimal(7,2)", true), "1,234.5", "", Cell{Text: "1234.50"}, ""},
		{col("mysql", "decimal(5,2)", true), "1.234", "", Cell{}, "has more than 2 decimal places"},
		{col("mysql", "decimal(5,2)", true), "1234.5", "", Cell{}, "has more than 3 digits before the point"},
		{col("mysql", "decimal(5,2)", true), "-12.5", "", Cell{Text: "-12.50"}, ""},
		{col("postgres", "numeric", true), "1e3", "", Cell{Text: "1e3"}, ""},
		{col("postgres", "double precision", true), "x1", "", Cell{}, "is not a number"},
		{col("postgres", "boolean", true), "Yes", "", Cell{Text: "true"}, ""},
		{col("mysql", "tinyint(1)", true), "f", "", Cell{Text: "0"}, ""},
		{col("mysql", "tinyint(1)", true), "maybe", "", Cell{}, "is not a boolean"},
		{col("mysql", "date", true), "2026/1/2", "", Cell{Text: "2026-01-02"}, ""},
		{col("mysql", "date", true), "2026-01-02 00:00:00", "", Cell{Text: "2026-01-02"}, ""},
		{col("mysql", "date", true), "2026-01-02 10:00", "", Cell{}, "is not a date"},
		{col("mysql", "date", true), "2026/13/01", "", Cell{}, "is not a date"},
		{col("mysql", "datetime", true), "2026-01-02", "", Cell{Text: "2026-01-02 00:00:00"}, ""},
		{col("mysql", "datetime", true), "2026/1/2 9:05", "", Cell{Text: "2026-01-02 09:05:00"}, ""},
		{col("mysql", "datetime", true), "2026-01-02T09:05:07.25", "", Cell{Text: "2026-01-02 09:05:07.25"}, ""},
		{col("mysql", "datetime", true), "2026-01-02T09:05:07Z", "", Cell{}, "is not a date and time"},
		{col("postgres", "timestamptz", true), "2026-01-02T09:05:07+08:00", "", Cell{Text: "2026-01-02 09:05:07+08:00"}, ""},
		{col("postgres", "timestamptz", true), "2026-01-02 09:05", "", Cell{Text: "2026-01-02 09:05:00"}, ""},
		{col("postgres", "time", true), "9:05", "", Cell{Text: "09:05:00"}, ""},
		{col("postgres", "time", true), "25:00", "", Cell{}, "is not a time"},
		{col("mysql", "varchar(3)", true), "中文字", "", Cell{Text: "中文字"}, ""},
		{col("mysql", "varchar(3)", true), "abcd", "", Cell{}, "is 4 characters, the column allows 3"},
		{col("mysql", "text", true), "  keep spaces ", "", Cell{Text: "  keep spaces "}, ""},
		{col("postgres", "jsonb", true), ` {"a":1}`, "", Cell{Text: ` {"a":1}`}, ""},
		// empty cells
		{col("mysql", "text", true), "", "", Cell{Null: true}, ""},
		{col("mysql", "text", true), "NULL", "NULL", Cell{Null: true}, ""},
		{col("mysql", "text", true), "", "NULL", Cell{Text: ""}, ""},
		{col("mysql", "int", true), "  ", "NULL", Cell{Null: true}, ""},
		{withDefault, "", "", Cell{Default: true}, ""},
		{col("mysql", "int", false), "", "", Cell{}, "cannot be empty"},
	}
	for _, tt := range tests {
		got, err := Convert(tt.target, tt.in, tt.null)
		switch {
		case tt.err == "" && err != nil:
			t.Errorf("Convert(%s, %q): %v", tt.target.Type, tt.in, err)
		case tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)):
			t.Errorf("Convert(%s, %q) error = %v, want %q", tt.target.Type, tt.in, err, tt.err)
		case tt.err == "" && got != tt.want:
			t.Errorf("Convert(%s, %q) = %+v, want %+v", tt.target.Type, tt.in, got, tt.want)
		}
	}
}

func TestRowError(t *testing.T) {
	e := &RowError{Line: 12, Column: "created", Value: "2026/13/01", Reason: "is not a date"}
	if got := e.Error(); got != `line 12, created: "2026/13/01" is not a date` {
		t.Fatalf("Error() = %s", got)
	}
	if got := (&RowError{Line: 3, Reason: "3 cells, the header has 2"}).Error(); got != "line 3: 3 cells, the header has 2" {
		t.Fatalf("Error() = %s", got)
	}
	long := &RowError{Line: 1, Column: "c", Value: strings.Repeat("x", 100), Reason: "is bad"}
	if !strings.Contains(long.Error(), "…") {
		t.Fatalf("long values are not clipped: %s", long.Error())
	}
}
