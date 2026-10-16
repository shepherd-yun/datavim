package sqlx

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func TestSplitOracle(t *testing.T) {
	texts := func(src string) []string {
		var out []string
		for _, s := range Split(src, Oracle) {
			out = append(out, s.Text)
		}
		return out
	}
	for _, tc := range []struct {
		src  string
		want []string
	}{
		{"select 1 from dual;\nbegin\n  null;\nend;\n/\nselect 2 from dual;", []string{"select 1 from dual", "begin\n  null;\nend;", "select 2 from dual"}},
		{"select ';' from dual", []string{"select ';' from dual"}},
		{"select q'[a;b]' from dual; select 2 from dual", []string{"select q'[a;b]' from dual", "select 2 from dual"}},
		{"select Q'{it's;}' x, q'!a;!' y from dual", []string{"select Q'{it's;}' x, q'!a;!' y from dual"}},
		{"declare x int; begin x := 1; end;", []string{"declare x int; begin x := 1; end;"}},
		{"CREATE OR REPLACE EDITIONABLE PACKAGE BODY p AS\n  PROCEDURE a IS BEGIN NULL; END;\nEND p;\n/\nselect 1 from dual", []string{"CREATE OR REPLACE EDITIONABLE PACKAGE BODY p AS\n  PROCEDURE a IS BEGIN NULL; END;\nEND p;", "select 1 from dual"}},
		{"create function f return number is begin return 1; end;\n  /  \n", []string{"create function f return number is begin return 1; end;"}},
		{"create table t (id int); create view v as select 1 x from dual;", []string{"create table t (id int)", "create view v as select 1 x from dual"}},
		{"begin null; end;\n-- a / in a comment\n/", []string{"begin null; end;\n-- a / in a comment"}},
	} {
		if diff := cmp.Diff(tc.want, texts(tc.src)); diff != "" {
			t.Errorf("Split(%q) (-want +got):\n%s", tc.src, diff)
		}
	}
}

func TestQuoteIfNeededOracle(t *testing.T) {
	for in, want := range map[string]string{
		"EMP": "EMP", "EMP_2$": "EMP_2$", "Emp": `"Emp"`, "emp": `"emp"`, "ORDER": `"ORDER"`, "a b": `"a b"`,
		"MixedCase": `"MixedCase"`, `x"y`: `"x""y"`, "1A": `"1A"`,
	} {
		if got := QuoteIfNeeded(in, Oracle); got != want {
			t.Errorf("QuoteIfNeeded(%q) = %s, want %s", in, got, want)
		}
	}
	if got := QuoteIdent(Oracle, `a"b`); got != `"a""b"` {
		t.Errorf("QuoteIdent = %s", got)
	}
	if got := QualifiedName(Oracle, db.TableRef{Namespace: db.Namespace{Database: "X", Schema: "HR"}, Name: "EMP"}); got != `"HR"."EMP"` {
		t.Errorf("QualifiedName = %s", got)
	}
}

func TestLiteralOracle(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		v    db.Value
		want string
	}{
		{"VARCHAR2", db.Value{Text: `it's \n`}, `'it''s \n'`},
		{"NUMBER", db.Value{Text: "1.5"}, "1.5"},
		{"RAW", db.Value{Text: "0AFF"}, "HEXTORAW('0AFF')"},
		{"DATE", db.Value{Text: "2026-01-02 03:04:05"}, "TO_DATE('2026-01-02 03:04:05', 'YYYY-MM-DD HH24:MI:SS')"},
		{"TIMESTAMP", db.Value{Text: "2026-01-02 03:04:05.5"}, "TO_TIMESTAMP('2026-01-02 03:04:05.5', 'YYYY-MM-DD HH24:MI:SS.FF')"},
		{"TimeStampTZ", db.Value{Text: "2026-01-02 03:04:05 +08:00"}, "TO_TIMESTAMP_TZ('2026-01-02 03:04:05 +08:00', 'YYYY-MM-DD HH24:MI:SS.FF TZH:TZM')"},
		{"NVARCHAR2", db.Value{Null: true}, "NULL"},
	} {
		if got := Literal(Oracle, tc.typ, tc.v); got != tc.want {
			t.Errorf("Literal(%s, %+v) = %s, want %s", tc.typ, tc.v, got, tc.want)
		}
	}
	if BeginSQL(Oracle) != "SET TRANSACTION READ WRITE" {
		t.Error(BeginSQL(Oracle))
	}
}

func TestTransactionEffectOracle(t *testing.T) {
	for sql, want := range map[string]TxEffect{
		"SET TRANSACTION READ WRITE":                   TxBegin,
		"set transaction isolation level serializable": TxBegin,
		"COMMIT":                   TxEnd,
		"commit work":              TxEnd,
		"ROLLBACK":                 TxEnd,
		"rollback work":            TxEnd,
		"ROLLBACK TO SAVEPOINT s1": TxResume,
		"rollback to s1":           TxResume,
		"SAVEPOINT s1":             TxNone,
		"begin null; end;":         TxNone,
		"END;":                     TxNone,
	} {
		if got := TransactionEffect(sql, Oracle); got != want {
			t.Errorf("TransactionEffect(%q) = %d, want %d", sql, got, want)
		}
	}
}

func TestAnalyzeOracle(t *testing.T) {
	for sql, want := range map[string]Kind{
		"begin null; end;":                      Other,
		"declare x int; begin null; end;":       Other,
		"ALTER SESSION SET CURRENT_SCHEMA = HR": Other,
		"execute immediate 'drop table t'":      DDL,
		"alter table t add x int":               DDL,
	} {
		if got := Analyze(sql, Oracle).Kind; got != want {
			t.Errorf("Analyze(%q) = %s, want %s", sql, got, want)
		}
	}
	var kinds []string
	for _, in := range AnalyzeBatch("begin update t set a = 1; execute immediate 'drop table x'; end;", Oracle) {
		s := in.Kind.String()
		if in.MissingWhere {
			s += "!"
		}
		kinds = append(kinds, s)
	}
	if diff := cmp.Diff([]string{"other", "write!", "ddl", "transaction"}, kinds); diff != "" {
		t.Errorf("AnalyzeBatch (-want +got):\n%s", diff)
	}
}

func TestSafeOtherOracle(t *testing.T) {
	for sql, want := range map[string]bool{
		"ALTER SESSION SET CURRENT_SCHEMA = HR":               true,
		"alter session set nls_date_format = 'YYYY-MM-DD'":    true,
		"ALTER SESSION SET TIME_ZONE = '+08:00'":              true,
		"SET TRANSACTION READ ONLY":                           true,
		"ALTER SESSION SET EVENTS '10046 trace name context'": false,
		"ALTER SESSION SET CURRENT_SCHEMA = HR; DROP TABLE t": false,
		"ALTER SYSTEM KILL SESSION '1,2'":                     false,
		"begin null; end;":                                    false,
		"call p()":                                            false,
		"EXEC p":                                              false,
	} {
		if got := SafeOther(sql, Oracle); got != want {
			t.Errorf("SafeOther(%q) = %v, want %v", sql, got, want)
		}
	}
}

func TestKeywordsOracle(t *testing.T) {
	kw := Keywords(Oracle)
	for _, w := range []string{"ROWNUM", "ROWID", "NVL", "SYSDATE", "DUAL", "CONNECT", "FETCH", "FIRST", "SELECT"} {
		if !slices.Contains(kw, w) {
			t.Errorf("Keywords(Oracle) lacks %s", w)
		}
	}
	if slices.Contains(kw, "LIMIT") || slices.Contains(kw, "TOP") {
		t.Error("Keywords(Oracle) offers LIMIT or TOP")
	}
	src := "SELECT * FROM t ORDER BY a "
	if diff := cmp.Diff([]string{"ASC", "DESC", "OFFSET"}, Following(src, len(src), Oracle).Words); diff != "" {
		t.Errorf("Following (-want +got):\n%s", diff)
	}
}
