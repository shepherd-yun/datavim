//go:build integration

package sqlserver_test

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcmssql "github.com/testcontainers/testcontainers-go/modules/mssql"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/db/dbtest"
	"github.com/shepherd-yun/sqlvim/internal/db/sqlserver"
	"github.com/shepherd-yun/sqlvim/internal/runner"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

var fixture = []string{
	`CREATE TABLE dbo.users (id bigint PRIMARY KEY, name nvarchar(100) NOT NULL, email nvarchar(100), note nvarchar(100) DEFAULT 'hi')`,
	`EXEC sp_addextendedproperty N'MS_Description', N'display name', N'SCHEMA', N'dbo', N'TABLE', N'users', N'COLUMN', N'name'`,
	`CREATE UNIQUE INDEX users_email_key ON dbo.users (email)`,
	`CREATE TABLE dbo.orders (id bigint PRIMARY KEY, user_id bigint NOT NULL,
	   CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES dbo.users (id))`,
	`CREATE TABLE dbo.[order] (id int)`,
	`CREATE TABLE dbo.[MixedCase] (id int)`,
	`CREATE VIEW dbo.active_users AS SELECT id, name FROM dbo.users`,
	`CREATE FUNCTION dbo.add_one(@x int) RETURNS int AS BEGIN RETURN @x + 1 END`,
	`INSERT INTO dbo.users VALUES (1, N'alice', N'a@x.com', DEFAULT), (2, N'李雷', N'', NULL), (3, N'bob', NULL, N'multi' + CHAR(10) + N'line')`,
}

func TestContract(t *testing.T) {
	ctx := context.Background()
	const password = "Sqlv1m:p@ss/w?#%"
	ctr, err := tcmssql.Run(ctx, "mcr.microsoft.com/mssql/server:2022-latest",
		tcmssql.WithAcceptEULA(),
		tcmssql.WithPassword(password),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "1433/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, _ := net.SplitHostPort(endpoint)
	port, _ := strconv.Atoi(portStr)
	open := func(ctx context.Context, database string) (db.Conn, error) {
		return sqlserver.Driver{}.Open(ctx, db.ConnConfig{
			Host: host, Port: port, User: "sa", Password: password, Database: database,
			Params: map[string]string{"encrypt": "disable"},
		})
	}

	master, err := open(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	// Readers do not wait for writers, as on MySQL and PostgreSQL: the
	// contract reads a row another session has changed in a transaction.
	for _, q := range []string{"CREATE DATABASE sqlvim_test", "ALTER DATABASE sqlvim_test SET READ_COMMITTED_SNAPSHOT ON"} {
		if _, err := master.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = master.Close()

	conn, err := open(ctx, "sqlvim_test")
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
		Dialect:  sqlx.SQLServer,
		NS:       db.Namespace{Database: "sqlvim_test", Schema: "dbo"},
		SleepSQL: "WAITFOR DELAY '00:00:30'",
		ManyRowsSQL: "SELECT a.n FROM (VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)) a(n), (VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)) b(n), " +
			"(VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)) c(n), (VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)) d(n), " +
			"(VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)) e(n), (VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)) f(n), " +
			"(VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)) g(n), (VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)) h(n)",
		OpenReadOnly: func(ctx context.Context) (db.Conn, error) { return open(ctx, "sqlvim_test") },
		TempTable: func(name string) (string, string) {
			return "CREATE TABLE #" + name + " (id int)", "#" + name
		},
		TextType:    "nvarchar(50)",
		NVarcharSQL: []string{"CREATE TABLE dbo.uni (id int PRIMARY KEY, name nvarchar(50))"},
	})

	t.Run("the console runs procedures and shows what they return", func(t *testing.T) {
		s, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		r := runner.RunOne(ctx, s, "EXEC sp_helpindex 'dbo.users'", sqlx.SQLServer, 100)
		if r.Err != nil || r.Rows == nil || len(r.Rows.Rows) == 0 {
			t.Fatalf("sp_helpindex: %+v", r)
		}
		r = runner.RunOne(ctx, s, "EXEC sp_executesql N'UPDATE dbo.users SET note = note WHERE id = 1'", sqlx.SQLServer, 100)
		if r.Err != nil {
			t.Fatalf("a procedure without a result set: %v", r.Err)
		}
	})
}
