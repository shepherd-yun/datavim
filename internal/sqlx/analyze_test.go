package sqlx

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestAnalyze(t *testing.T) {
	cases := []struct {
		d    Dialect
		sql  string
		want Info
	}{
		{MySQL, "select * from users", Info{Kind: Read, ReturnsRows: true, Target: "users"}},
		{Postgres, "SELECT * FROM app.users u WHERE id = 1", Info{Kind: Read, ReturnsRows: true, Target: "users"}},
		{MySQL, "select a, b from t order by a, b", Info{Kind: Read, ReturnsRows: true, Target: "t"}},
		{MySQL, "select * from a join b on a.id = b.id", Info{Kind: Read, ReturnsRows: true}},
		{MySQL, "select * from a, b", Info{Kind: Read, ReturnsRows: true}},
		{MySQL, "select 1", Info{Kind: Read, ReturnsRows: true}},
		{MySQL, "select * from t for update", Info{Kind: Read, ReturnsRows: true, Target: "t"}},
		{MySQL, "(select 1) union (select 2)", Info{Kind: Read, ReturnsRows: true}},
		{MySQL, "select 1 into @x", Info{Kind: Write}},
		{Postgres, "select * into new_t from t", Info{Kind: Write}},
		{MySQL, "show tables", Info{Kind: Read, ReturnsRows: true}},
		{MySQL, "desc users", Info{Kind: Read, ReturnsRows: true}},
		{Postgres, "values (1), (2)", Info{Kind: Read, ReturnsRows: true}},
		{Postgres, "explain select 1", Info{Kind: Read, ReturnsRows: true}},
		{Postgres, "explain (analyze) delete from t", Info{Kind: Write, ReturnsRows: true, MissingWhere: true, Target: "t"}},
		{MySQL, "  /* hi */ delete from t", Info{Kind: Write, MissingWhere: true, Target: "t"}},
		{MySQL, "DELETE FROM `order` WHERE id = 1", Info{Kind: Write, Target: "order"}},
		{MySQL, "delete low_priority quick from app.t limit 10", Info{Kind: Write, MissingWhere: true, Target: "t"}},
		{Postgres, `update "Users" set a = 1`, Info{Kind: Write, MissingWhere: true, Target: "Users"}},
		{Postgres, "update only t set a = 1 where id = 2", Info{Kind: Write, Target: "t"}},
		{MySQL, "UPDATE t SET a = (SELECT 1 FROM x WHERE y = 1)", Info{Kind: Write, MissingWhere: true, Target: "t"}},
		{MySQL, "update t set note = 'where'", Info{Kind: Write, MissingWhere: true, Target: "t"}},
		{MySQL, "update t set note = 1 -- where id = 1", Info{Kind: Write, MissingWhere: true, Target: "t"}},
		{Postgres, "insert into s.t (a) values (1) returning id", Info{Kind: Write, ReturnsRows: true, Target: "t"}},
		{MySQL, "insert ignore into t values (1) on duplicate key update a = 1", Info{Kind: Write, Target: "t"}},
		{MySQL, "insert t values (1)", Info{Kind: Write, Target: "t"}},
		{MySQL, "replace into t values (1)", Info{Kind: Write, Target: "t"}},
		{Postgres, "merge into t using s on t.id = s.id when matched then update set a = 1", Info{Kind: Write, Target: "t"}},
		{Postgres, "with d as (delete from t returning *) select * from d", Info{Kind: Write, ReturnsRows: true, MissingWhere: true, Target: "t"}},
		{Postgres, "with d as (delete from t where id = 1 returning *) select * from d", Info{Kind: Write, ReturnsRows: true}},
		{Postgres, "with x as (select 1) select * from x", Info{Kind: Read, ReturnsRows: true}},
		{Postgres, "with recursive x(n) as (select 1 union all select n + 1 from x) delete from t", Info{Kind: Write, MissingWhere: true, Target: "t"}},
		{MySQL, "call p(1)", Info{Kind: Write, ReturnsRows: true}},
		{Postgres, "copy t from stdin", Info{Kind: Write}},
		{MySQL, "create table t (id int, p int references q (id) on delete cascade)", Info{Kind: DDL}},
		{MySQL, "DROP TABLE t", Info{Kind: DDL}},
		{MySQL, "truncate t", Info{Kind: DDL}},
		{Postgres, "begin", Info{Kind: Tx}},
		{MySQL, "start transaction", Info{Kind: Tx}},
		{Postgres, "set search_path = app", Info{Kind: Other}},
		{MySQL, "use app", Info{Kind: Other}},
		{MySQL, "", Info{Kind: Other}},
		{MySQL, "-- only a comment", Info{Kind: Other}},
	}
	for _, c := range cases {
		if got := Analyze(c.sql, c.d); got != c.want {
			t.Errorf("Analyze(%q)\n got  %+v\n want %+v", c.sql, got, c.want)
		}
	}
}

func TestKindString(t *testing.T) {
	for k, want := range map[Kind]string{Other: "other", Read: "read", Write: "write", DDL: "ddl", Tx: "transaction"} {
		if k.String() != want {
			t.Errorf("%d = %q", k, k.String())
		}
	}
}

func TestUnlocksReadOnly(t *testing.T) {
	cases := []struct {
		d    Dialect
		sql  string
		want bool
	}{
		{Postgres, "set default_transaction_read_only = off", true},
		{Postgres, "SET SESSION CHARACTERISTICS AS TRANSACTION READ WRITE", true},
		{Postgres, "set /* x */ transaction_read_only to off", true},
		{Postgres, "select set_config('default_transaction_read_only', 'off', false)", true},
		{Postgres, "begin read/**/write", true},
		{MySQL, "SET SESSION TRANSACTION READ WRITE", true},
		{MySQL, "set transaction_read_only = 0", true},
		{MySQL, "set @@session.tx_read_only = default", true},
		{MySQL, "start transaction read write", true},
		{Postgres, "select 1", false},
		{Postgres, "set search_path to app", false},
		{Postgres, "start transaction read only", false},
		{MySQL, "select 'read write' as phrase", false},
	}
	for _, c := range cases {
		if got := UnlocksReadOnly(c.sql, c.d); got != c.want {
			t.Errorf("UnlocksReadOnly(%q) = %v, want %v", c.sql, got, c.want)
		}
	}
}

func TestAnalyzeBatch(t *testing.T) {
	kinds := func(sql string) []string {
		var out []string
		for _, in := range AnalyzeBatch(sql, SQLServer) {
			s := in.Kind.String()
			if in.MissingWhere {
				s += "!"
			}
			out = append(out, s+":"+in.SQL)
		}
		return out
	}
	for _, tc := range []struct {
		sql  string
		want []string
	}{
		{"select 1", []string{"read:select 1"}},
		{"SELECT 1 DELETE FROM t WHERE id=1", []string{"read:SELECT 1", "write:DELETE FROM t WHERE id=1"}},
		{"select 'delete from t' /* update t */", []string{"read:select 'delete from t' /* update t */"}},
		{"BEGIN UPDATE t SET a=1 END", []string{"transaction:BEGIN", "write!:UPDATE t SET a=1 END"}},
		{"select * from t for update", []string{"read:select * from t for update"}},
		{"EXEC p DELETE t", []string{"other:EXEC p", "write!:DELETE t"}},
		{"select x.delete from t x", []string{"read:select x.delete from t x"}},
	} {
		if diff := cmp.Diff(tc.want, kinds(tc.sql)); diff != "" {
			t.Errorf("AnalyzeBatch(%q) (-want +got):\n%s", tc.sql, diff)
		}
	}
}

func TestSafeOtherSQLServer(t *testing.T) {
	for sql, want := range map[string]bool{
		"USE crm": true, "SET NOCOUNT ON": true, "set @a = 1": true, "DECLARE @a int = 1": true, "PRINT 1": true,
		"EXEC sp_helptext 'v'": true, "EXEC sys.sp_spaceused": true, "EXECUTE sp_databases": true,
		"EXEC dbo.sp_help": false, "EXEC usp_x": false, "EXEC('select 1')": false, "DBCC CHECKDB": false,
		"IF 1=1 PRINT 1": false, "WAITFOR DELAY '00:00:01'": false,
	} {
		if got := SafeOther(sql, SQLServer); got != want {
			t.Errorf("SafeOther(%q) = %v, want %v", sql, got, want)
		}
	}
	if SafeOther("SET NOCOUNT ON", MySQL) {
		t.Error("MySQL has no allowlist")
	}
}
