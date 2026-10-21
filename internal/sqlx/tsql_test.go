package sqlx

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
)

func TestQuoteIdentSQLServer(t *testing.T) {
	for in, want := range map[string]string{"a]b": "[a]]b]", "t": "[t]", "x y": "[x y]"} {
		if got := QuoteIdent(SQLServer, in); got != want {
			t.Errorf("QuoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"users": "users", "Users": "Users", "order": "[order]", "a b": "[a b]", "x]": "[x]]]"} {
		if got := QuoteIfNeeded(in, SQLServer); got != want {
			t.Errorf("QuoteIfNeeded(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQualifiedNameSQLServer(t *testing.T) {
	for _, tc := range []struct {
		ns   db.Namespace
		want string
	}{
		{db.Namespace{Database: "crm", Schema: "dbo"}, "[crm].[dbo].[t]"},
		{db.Namespace{Schema: "dbo"}, "[dbo].[t]"},
		{db.Namespace{Database: "crm"}, "[crm]..[t]"},
		{db.Namespace{}, "[t]"},
	} {
		if got := QualifiedName(SQLServer, db.TableRef{Namespace: tc.ns, Name: "t"}); got != tc.want {
			t.Errorf("QualifiedName(%+v) = %q, want %q", tc.ns, got, tc.want)
		}
	}
}

func TestLiteralSQLServer(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		v    db.Value
		want string
	}{
		{"NVARCHAR", db.Value{Text: "it's"}, "N'it''s'"},
		{"NVARCHAR", db.Value{Text: "张三"}, "N'张三'"},
		{"VARCHAR", db.Value{Text: `a\b`}, `N'a\b'`},
		{"VARBINARY", db.Value{Text: "0x0A0B"}, "0x0A0B"},
		{"INT", db.Value{Text: "42"}, "42"},
		{"MONEY", db.Value{Text: "1.50"}, "1.50"},
		{"NVARCHAR", db.Value{Null: true}, "NULL"},
	} {
		if got := Literal(SQLServer, tc.typ, tc.v); got != tc.want {
			t.Errorf("Literal(%s, %+v) = %q, want %q", tc.typ, tc.v, got, tc.want)
		}
	}
	if got := BeginSQL(SQLServer); got != "BEGIN TRANSACTION" {
		t.Errorf("BeginSQL = %q", got)
	}
}

func TestLexSQLServer(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want []string // code token texts
	}{
		{"select [a]]b] from t", []string{"select", "a]b", "from", "t"}},
		{`select "x" from t`, []string{"select", "x", "from", "t"}},
		{"select N'it''s' -- c\n, 1", []string{"select", "N'it''s'", ",", "1"}},
		{"select /* a /* nested */ still */ 1", []string{"select", "1"}},
		{"select 'a\\' , 2", []string{"select", "'a\\'", ",", "2"}},
		{"select @x, #t, ##g", []string{"select", "@x", ",", "#t", ",", "##g"}},
	} {
		var got []string
		for _, tk := range codeTokens(tc.src, SQLServer) {
			got = append(got, tk.text)
		}
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("codeTokens(%q) (-want +got):\n%s", tc.src, diff)
		}
	}
}

func TestSplitGO(t *testing.T) {
	texts := func(src string) []string {
		var out []string
		for _, s := range Split(src, SQLServer) {
			out = append(out, s.Text)
		}
		return out
	}
	for _, tc := range []struct {
		src  string
		want []string
	}{
		{"select 1\nGO\nselect 2", []string{"select 1", "select 2"}},
		{"select 1\ngo\nselect 2\n", []string{"select 1", "select 2"}},
		{"select 'GO'\n", []string{"select 'GO'"}},
		{"select 1\n  go 5  \nselect 2", []string{"select 1", "select 2"}},
		{"select 1; select 2", []string{"select 1", "select 2"}},
		{"select 1 /*\nGO\n*/ select 2", []string{"select 1 /*\nGO\n*/ select 2"}},
		{"select 1\nGOTO x", []string{"select 1\nGOTO x"}},
		{"select 1 go", []string{"select 1 go"}},
	} {
		if diff := cmp.Diff(tc.want, texts(tc.src)); diff != "" {
			t.Errorf("Split(%q) (-want +got):\n%s", tc.src, diff)
		}
	}
	if got := Split("select 1\nGO\nselect 2", MySQL); len(got) != 1 {
		t.Errorf("GO splits MySQL: %v", got)
	}
}

func TestTransactionEffectSQLServer(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		want TxEffect
	}{
		{"BEGIN TRAN", TxBegin},
		{"begin transaction t1", TxBegin},
		{"BEGIN DISTRIBUTED TRANSACTION", TxBegin},
		{"BEGIN", TxNone},
		{"BEGIN TRY SELECT 1 END TRY BEGIN CATCH END CATCH", TxNone},
		{"COMMIT", TxEnd},
		{"COMMIT TRAN", TxEnd},
		{"commit transaction t1", TxEnd},
		{"COMMIT WORK", TxEnd},
		{"ROLLBACK", TxEnd},
		{"ROLLBACK TRAN", TxEnd},
		{"ROLLBACK TRANSACTION", TxEnd},
		{"ROLLBACK TRAN s1", TxResume},
		{"SAVE TRAN s1", TxNone},
		{"END", TxNone},
	} {
		if got := TransactionEffect(tc.sql, SQLServer); got != tc.want {
			t.Errorf("TransactionEffect(%q) = %d, want %d", tc.sql, got, tc.want)
		}
	}
}

func TestFilteredSQLServerOrderBy(t *testing.T) {
	cols := []string{"id", "name"}
	for _, tc := range []struct {
		stmt, want string
	}{
		{"select * from t order by id", "SELECT * FROM (\nselect * from t\n) q WHERE (a = 1\n)\norder by id"},
		{"select top 5 * from t order by id", "SELECT * FROM (\nselect top 5 * from t order by id\n) q WHERE (a = 1\n)"},
		{"select * from t order by id offset 5 rows fetch next 5 rows only", "SELECT * FROM (\nselect * from t order by id offset 5 rows fetch next 5 rows only\n) q WHERE (a = 1\n)"},
	} {
		got, ordered, err := Filtered(SQLServer, tc.stmt, "a = 1", false, cols)
		if err != nil || !ordered || got != tc.want {
			t.Errorf("Filtered(%q) = %q, %v, %v; want %q", tc.stmt, got, ordered, err, tc.want)
		}
	}
}

func TestKeywordsSQLServer(t *testing.T) {
	kw := Keywords(SQLServer)
	for _, w := range []string{"TOP", "OFFSET", "FETCH", "NEXT", "ROWS", "ONLY", "NOLOCK", "OUTPUT", "MERGE", "EXEC", "SELECT"} {
		if !slices.Contains(kw, w) {
			t.Errorf("Keywords(SQLServer) lacks %s", w)
		}
	}
	if slices.Contains(kw, "LIMIT") {
		t.Error("Keywords(SQLServer) offers LIMIT")
	}
}

func TestFollowingSQLServer(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want []string
	}{
		{"SELECT ", []string{"*", "DISTINCT", "TOP"}},
		{"SELECT * FROM t ", []string{"WHERE", "JOIN", "LEFT JOIN", "INNER JOIN", "GROUP BY", "ORDER BY", "AS"}},
		{"SELECT * FROM t WHERE a = 1 ", []string{"AND", "OR", "GROUP BY", "ORDER BY"}},
		{"SELECT * FROM t ORDER BY a ", []string{"ASC", "DESC", "OFFSET"}},
		{"SELECT * FROM t ORDER BY a DESC ", []string{"OFFSET"}},
	} {
		if diff := cmp.Diff(tc.want, Following(tc.src, len(tc.src), SQLServer).Words); diff != "" {
			t.Errorf("Following(%q) (-want +got):\n%s", tc.src, diff)
		}
	}
}
