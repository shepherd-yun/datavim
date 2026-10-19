//go:build integration

package oracle_test

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/db/dbtest"
	"github.com/shepherd-yun/sqlvim/internal/db/oracle"
	"github.com/shepherd-yun/sqlvim/internal/runner"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// Table names are quoted lower case like the other engines' fixtures;
// columns are unquoted and so stored in upper case (FoldsUpper).
var fixture = []string{
	`CREATE TABLE "users" (id NUMBER(19) PRIMARY KEY, name NVARCHAR2(100) NOT NULL, email VARCHAR2(100), note VARCHAR2(100) DEFAULT 'hi')`,
	`COMMENT ON COLUMN "users".name IS 'display name'`,
	`CREATE UNIQUE INDEX "users_email_key" ON "users" (email)`,
	`CREATE TABLE "orders" (id NUMBER(19) PRIMARY KEY, user_id NUMBER(19) NOT NULL,
	   CONSTRAINT "orders_user_fk" FOREIGN KEY (user_id) REFERENCES "users" (id))`,
	`CREATE TABLE "order" (id INT)`,
	`CREATE TABLE "MixedCase" (id INT)`,
	`CREATE VIEW "active_users" AS SELECT id, name FROM "users"`,
	"CREATE FUNCTION \"add_one\"(x NUMBER) RETURN NUMBER IS BEGIN RETURN x + 1; END;",
	`INSERT INTO "users" VALUES (1, N'alice', 'a@x.com', DEFAULT)`,
	`INSERT INTO "users" VALUES (2, N'李雷', '', NULL)`,
	`INSERT INTO "users" VALUES (3, N'bob', NULL, 'multi' || CHR(10) || 'line')`,
}

func TestContract(t *testing.T) {
	ctx := context.Background()
	const password = "p@ss:w/rd?#%"
	ctr, err := testcontainers.Run(ctx, "gvenzl/oracle-free:23-slim",
		testcontainers.WithExposedPorts("1521/tcp"),
		testcontainers.WithEnv(map[string]string{
			"ORACLE_PASSWORD": "Sys0racle", "APP_USER": "sqlvim", "APP_USER_PASSWORD": password,
		}),
		testcontainers.WithWaitStrategy(wait.ForLog("DATABASE IS READY TO USE!").WithStartupTimeout(5*time.Minute)),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "1521/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, _ := net.SplitHostPort(endpoint)
	port, _ := strconv.Atoi(portStr)
	conn, err := oracle.Driver{}.Open(ctx, db.ConnConfig{
		Host: host, Port: port, User: "sqlvim", Password: password, Database: "FREEPDB1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, stmt := range fixture {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	dbtest.RunContract(t, conn, dbtest.Fixture{
		Dialect:     sqlx.Oracle,
		NS:          db.Namespace{Schema: "SQLVIM"},
		EmptyIsNull: true,
		FoldsUpper:  true,
		SleepSQL:    "SELECT COUNT(*) FROM all_objects a, all_objects b, all_objects c",
		ManyRowsSQL: "SELECT level FROM dual CONNECT BY level <= 100000000",
		TempTable: func(name string) (string, string) {
			table := "ORA$PTT_" + name
			return "CREATE PRIVATE TEMPORARY TABLE " + table + " (id INT) ON COMMIT PRESERVE DEFINITION", table
		},
		TextType:    "VARCHAR2(50)",
		NVarcharSQL: []string{`CREATE TABLE "uni" (id INT PRIMARY KEY, name NVARCHAR2(50))`},
	})

	t.Run("a console transaction stays open until COMMIT or ROLLBACK", func(t *testing.T) {
		if _, err := conn.Exec(ctx, `CREATE TABLE "tx_t" (id INT)`); err != nil {
			t.Fatal(err)
		}
		s, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		count := func(q db.Querier) string {
			t.Helper()
			rs, err := q.Query(ctx, `SELECT COUNT(*) FROM "tx_t"`)
			if err != nil {
				t.Fatal(err)
			}
			return rs.Rows[0][0].Text
		}
		for _, q := range []string{"SET TRANSACTION READ WRITE", `INSERT INTO "tx_t" VALUES (1)`} {
			if _, err := s.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		if n := count(conn); n != "0" {
			t.Fatalf("another connection sees the uncommitted row: %s", n)
		}
		if n := count(s); n != "1" {
			t.Fatalf("the session sees its row: %s", n)
		}
		if _, err := s.Exec(ctx, "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
		if n := count(s); n != "0" {
			t.Fatalf("after ROLLBACK: %s", n)
		}
		// outside a transaction each statement commits
		if _, err := s.Exec(ctx, `INSERT INTO "tx_t" VALUES (2)`); err != nil {
			t.Fatal(err)
		}
		if n := count(conn); n != "1" {
			t.Fatalf("autocommit: %s", n)
		}
	})

	t.Run("mixed-case names round-trip through staged edits", func(t *testing.T) {
		if _, err := conn.Exec(ctx, `CREATE TABLE "MixedEdit" ("Id" INT PRIMARY KEY, "Name" VARCHAR2(20))`); err != nil {
			t.Fatal(err)
		}
		ref := db.TableRef{Namespace: db.Namespace{Schema: "SQLVIM"}, Name: "MixedEdit"}
		if _, err := conn.Exec(ctx, `INSERT INTO `+sqlx.QualifiedName(sqlx.Oracle, ref)+` VALUES (1, 'a')`); err != nil {
			t.Fatal(err)
		}
		up := `UPDATE ` + sqlx.QualifiedName(sqlx.Oracle, ref) + ` SET ` + sqlx.QuoteIfNeeded("Name", sqlx.Oracle) +
			` = 'b' WHERE ` + sqlx.QuoteIfNeeded("Id", sqlx.Oracle) + ` = 1`
		if res, err := conn.Exec(ctx, up); err != nil || res.RowsAffected != 1 {
			t.Fatalf("%s: %v %v", up, res, err)
		}
	})

	t.Run("the console runs CALL and PL/SQL blocks", func(t *testing.T) {
		if _, err := conn.Exec(ctx, "CREATE PROCEDURE sqlvim_noop IS BEGIN NULL; END;"); err != nil {
			t.Fatal(err)
		}
		s, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		for _, st := range sqlx.Split("CALL sqlvim_noop();\nbegin\n  sqlvim_noop;\nend;\n/\n", sqlx.Oracle) {
			if r := runner.RunOne(ctx, s, st.Text, sqlx.Oracle, 100); r.Err != nil {
				t.Fatalf("%s: %v", st.Text, r.Err)
			}
		}
	})

	// The server tab reads v$ views and kills sessions: grant what a DBA
	// account has, then reconnect so the role applies.
	sys, err := oracle.Driver{}.Open(ctx, db.ConnConfig{
		Host: host, Port: port, User: "system", Password: "Sys0racle", Database: "FREEPDB1",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{"GRANT SELECT_CATALOG_ROLE TO sqlvim", "GRANT ALTER SYSTEM TO sqlvim"} {
		if _, err := sys.Exec(ctx, g); err != nil {
			t.Fatalf("%s: %v", g, err)
		}
	}
	_ = sys.Close()
	srvConn, err := oracle.Driver{}.Open(ctx, db.ConnConfig{
		Host: host, Port: port, User: "sqlvim", Password: password, Database: "FREEPDB1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srvConn.Close() })
	dbtest.RunServerContract(t, srvConn, dbtest.ServerFixture{
		SleepSQL:  "SELECT /* sqlvim_contract_sleep */ COUNT(*) FROM all_objects a, all_objects b, all_objects c",
		LockSQL:   `UPDATE "users" SET name = name WHERE id = 1`,
		Begin:     "SET TRANSACTION READ WRITE",
		SelfIDSQL: "SELECT sys_context('USERENV', 'SID') FROM dual",
		Setting:   "processes",
	})
}
