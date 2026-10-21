package runner

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/sqlx"
)

func TestGuardSQLServerReadOnly(t *testing.T) {
	ro := Policy{ReadOnly: true}
	for _, tc := range []struct {
		sql  string
		want Verdict
	}{
		{"SELECT 1", Allow},
		{"USE crm", Allow},
		{"SET NOCOUNT ON", Allow},
		{"SET @x = 1", Allow},
		{"DECLARE @a int", Allow},
		{"PRINT 'hi'", Allow},
		{"EXEC sp_help 'dbo.t'", Allow},
		{"exec sys.sp_columns 't'", Allow},
		{"EXECUTE sp_who2", Allow},
		{"select 'DELETE FROM t'", Allow},
		{"-- delete from t\nselect 1", Allow},
		{"SELECT a FROM t WITH (NOLOCK) ORDER BY a OFFSET 0 ROWS FETCH NEXT 5 ROWS ONLY", Allow},
		{"EXEC dbo.usp_clean", Reject},
		{"EXEC sp_rename 't', 'u'", Reject},
		{"EXECUTE('delete from t')", Reject},
		{"SELECT 1 DELETE FROM t WHERE id=1", Reject},
		{"BEGIN DELETE FROM t WHERE id = 1 END", Reject},
		{"MERGE t USING s ON t.id = s.id WHEN MATCHED THEN UPDATE SET a = 1;", Reject},
		{"SELECT * INTO x FROM t", Reject},
		{"SELECT 1 SHUTDOWN", Reject},
		{"DBCC CHECKDB", Reject},
		{"IF 1 = 1 SELECT 1", Reject},
	} {
		stmts := sqlx.Split(tc.sql, sqlx.SQLServer)
		c := Guard(stmts, sqlx.SQLServer, ro)
		if c.Verdict != tc.want {
			t.Errorf("Guard(%q) = %v (%s), want %v", tc.sql, c.Verdict, c.Reason, tc.want)
		}
	}
	c := Guard(sqlx.Split("EXEC dbo.usp_clean", sqlx.SQLServer), sqlx.SQLServer, ro)
	if want := "read-only connection: EXEC statements are blocked on sqlserver (:set noreadonly allows them for this session)"; c.Reason != want {
		t.Errorf("reason = %q, want %q", c.Reason, want)
	}
	// MySQL keeps the server-side guard and its looser client rules.
	if c := Guard(sqlx.Split("CALL p()", sqlx.MySQL), sqlx.MySQL, ro); c.Verdict != Reject {
		t.Errorf("MySQL CALL = %v", c.Verdict)
	}
	if c := Guard(sqlx.Split("FLUSH TABLES", sqlx.MySQL), sqlx.MySQL, ro); c.Verdict != Allow {
		t.Errorf("MySQL unknown statement = %v, want Allow (server read-only covers it)", c.Verdict)
	}
}

func TestGuardSQLServerMissingWhere(t *testing.T) {
	prod := Policy{Prod: true}
	c := Guard(sqlx.Split("SELECT 1 UPDATE t SET a=1", sqlx.SQLServer), sqlx.SQLServer, prod)
	if c.Verdict != Confirm || c.Expect != "t" {
		t.Fatalf("check = %+v", c)
	}
	c = Guard(sqlx.Split("UPDATE t SET a=1 WHERE id=1 DELETE FROM u", sqlx.SQLServer), sqlx.SQLServer, prod)
	if c.Verdict != Confirm || c.Expect != "u" {
		t.Fatalf("second statement: %+v", c)
	}
	c = Guard(sqlx.Split("SELECT 1 INSERT INTO t VALUES (1)", sqlx.SQLServer), sqlx.SQLServer, prod)
	if c.Verdict != Confirm || c.Expect != "" || !strings.Contains(strings.Join(c.Body, "\n"), "write t") {
		t.Fatalf("prod write in a batch: %+v", c)
	}
	c = Guard(sqlx.Split("MERGE t USING s ON t.id = s.id WHEN MATCHED THEN UPDATE SET a = 1 WHEN NOT MATCHED THEN INSERT (a) VALUES (1);", sqlx.SQLServer), sqlx.SQLServer, prod)
	if c.Verdict != Confirm || c.Expect != "" {
		t.Fatalf("MERGE's UPDATE is part of it, not an UPDATE without WHERE: %+v", c)
	}
	c = Guard(sqlx.Split("GRANT SELECT, INSERT, UPDATE ON t TO u", sqlx.SQLServer), sqlx.SQLServer, prod)
	if c.Expect != "" {
		t.Fatalf("GRANT lists privileges, not statements: %+v", c)
	}
}

func TestGuardOracleReadOnly(t *testing.T) {
	ro := Policy{ReadOnly: true}
	for _, tc := range []struct {
		sql  string
		want Verdict
	}{
		{"ALTER SESSION SET CURRENT_SCHEMA = HR", Allow},
		{"select * from dual", Allow},
		{"SET TRANSACTION READ ONLY", Allow},
		{"explain plan for delete from t", Allow},
		{"begin delete from t where id=1; end;", Reject},
		{"begin null; end;", Reject},
		{"call p()", Reject},
		{"begin execute immediate 'drop table t'; end;", Reject},
		{"SET TRANSACTION READ WRITE", Reject},
		{"ALTER SYSTEM FLUSH SHARED_POOL", Reject},
	} {
		c := Guard(sqlx.Split(tc.sql, sqlx.Oracle), sqlx.Oracle, ro)
		if c.Verdict != tc.want {
			t.Errorf("Guard(%q) = %v (%s), want %v", tc.sql, c.Verdict, c.Reason, tc.want)
		}
	}
	c := Guard(sqlx.Split("begin update t set a = 1; end;", sqlx.Oracle), sqlx.Oracle, Policy{Prod: true})
	if c.Verdict != Confirm || c.Expect != "t" {
		t.Fatalf("UPDATE without WHERE inside a block: %+v", c)
	}
}

func TestGuardProdConfirmsUnknownStatementsOnStrictDialects(t *testing.T) {
	prod := Policy{Prod: true}
	for _, tc := range []struct {
		d    sqlx.Dialect
		sql  string
		want Verdict
	}{
		{sqlx.SQLServer, "EXEC dbo.usp_purge", Confirm},
		{sqlx.SQLServer, "EXEC sp_help 'dbo.t'", Allow},
		{sqlx.SQLServer, "SET NOCOUNT ON", Allow},
		{sqlx.Oracle, "begin p; end;", Confirm},
		{sqlx.Oracle, "ALTER SESSION SET CURRENT_SCHEMA = HR", Allow},
		{sqlx.MySQL, "FLUSH TABLES", Allow},
	} {
		if c := Guard(sqlx.Split(tc.sql, tc.d), tc.d, prod); c.Verdict != tc.want {
			t.Errorf("%s %q = %v, want %v", tc.d.Driver(), tc.sql, c.Verdict, tc.want)
		}
	}
}
