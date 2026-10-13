package sqlx

import (
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func TestLiteral(t *testing.T) {
	for _, c := range []struct {
		d    Dialect
		typ  string
		v    db.Value
		want string
	}{
		{MySQL, "VARCHAR", db.Value{Null: true}, "NULL"},
		{MySQL, "BIGINT", db.Value{Text: "42"}, "42"},
		{MySQL, "UNSIGNED INT", db.Value{Text: "7"}, "7"},
		{Postgres, "INT8", db.Value{Text: "-1.5e3"}, "-1.5e3"},
		{MySQL, "BIGINT", db.Value{Text: "4 2"}, "'4 2'"},
		{MySQL, "VARCHAR", db.Value{Text: `it's \n`}, `'it''s \\n'`},
		{Postgres, "TEXT", db.Value{Text: `it's \n`}, `'it''s \n'`},
		{MySQL, "VARCHAR", db.Value{Text: ""}, "''"},
		{MySQL, "VARBINARY", db.Value{Text: "0x6869"}, "0x6869"},
		{Postgres, "BYTEA", db.Value{Text: `\x6869`}, `'\x6869'`},
		{Postgres, "TEXT", db.Value{Text: "李\n雷"}, "'李\n雷'"},
		{Postgres, "", db.Value{Text: "(0,1)"}, "'(0,1)'"},
	} {
		if got := Literal(c.d, c.typ, c.v); got != c.want {
			t.Errorf("Literal(%v, %q, %+v) = %s, want %s", c.d, c.typ, c.v, got, c.want)
		}
	}
}

func TestQuoteIdentAndQualifiedName(t *testing.T) {
	if got := QuoteIdent(MySQL, "a`b"); got != "`a``b`" {
		t.Errorf("mysql: %s", got)
	}
	if got := QuoteIdent(Postgres, `a"b`); got != `"a""b"` {
		t.Errorf("postgres: %s", got)
	}
	ref := db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "s"}, Name: "users"}
	if got := QualifiedName(MySQL, ref); got != "`app`.`users`" {
		t.Errorf("mysql qualified: %s", got)
	}
	if got := QualifiedName(Postgres, ref); got != `"s"."users"` {
		t.Errorf("postgres qualified: %s", got)
	}
	bare := db.TableRef{Name: "users"}
	if QualifiedName(MySQL, bare) != "`users`" || QualifiedName(Postgres, bare) != `"users"` {
		t.Error("no namespace: just the table")
	}
}

func TestBeginSQL(t *testing.T) {
	if BeginSQL(MySQL) != "START TRANSACTION" || BeginSQL(Postgres) != "BEGIN" {
		t.Fatal("BeginSQL")
	}
}

func TestTypeHelpers(t *testing.T) {
	for _, typ := range []string{"INT", "UNSIGNED BIGINT", "DECIMAL", "int8", "numeric", "float8", "DOUBLE"} {
		if !IsNumericType(typ) {
			t.Errorf("%s is numeric", typ)
		}
	}
	for _, typ := range []string{"interval", "VARCHAR", "text", "point"} {
		if IsNumericType(typ) {
			t.Errorf("%s is not numeric", typ)
		}
	}
	for s, want := range map[string]bool{"1": true, "-2.5": true, "1e10": true, "NaN": false, "Infinity": false, "1.": false, "": false} {
		if IsNumber(s) != want {
			t.Errorf("IsNumber(%q) != %v", s, want)
		}
	}
	if !IsBinaryType("VARBINARY") || !IsBinaryType("LONGBLOB") || IsBinaryType("bytea") {
		t.Error("IsBinaryType")
	}
	if NormType("unsigned int") != "INT" {
		t.Error("NormType")
	}
}
