//go:build integration

package postgres_test

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/db/dbtest"
	"github.com/shepherd-yun/sqlvim/internal/db/postgres"
)

var fixture = []string{
	`CREATE SCHEMA app`,
	`CREATE TABLE app.users (id bigint PRIMARY KEY, name text NOT NULL, email text, note text DEFAULT 'hi')`,
	`COMMENT ON COLUMN app.users.name IS 'display name'`,
	`CREATE UNIQUE INDEX users_email_key ON app.users (email)`,
	`CREATE TABLE app.orders (id bigint PRIMARY KEY, user_id bigint NOT NULL,
	   CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES app.users (id))`,
	`CREATE TABLE app."order" (id int)`,
	`CREATE TABLE app."MixedCase" (id int)`,
	`CREATE VIEW app.active_users AS SELECT id, name FROM app.users`,
	`INSERT INTO app.users VALUES (1, 'alice', 'a@x.com', DEFAULT), (2, '李雷', '', NULL), (3, 'bob', NULL, E'multi\nline')`,
}

func TestContract(t *testing.T) {
	ctx := context.Background()
	const password = "p@ss:w/rd?#%"
	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("app"),
		tcpostgres.WithUsername("sqlvim"),
		tcpostgres.WithPassword(password),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "5432/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, _ := net.SplitHostPort(endpoint)
	port, _ := strconv.Atoi(portStr)

	conn, err := postgres.Driver{}.Open(ctx, db.ConnConfig{
		Host: host, Port: port, User: "sqlvim", Password: password, Database: "app",
		Params: map[string]string{"sslmode": "disable"},
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
	open := func(ctx context.Context, readOnly bool) (db.Conn, error) {
		return postgres.Driver{}.Open(ctx, db.ConnConfig{
			Host: host, Port: port, User: "sqlvim", Password: password, Database: "app",
			Params: map[string]string{"sslmode": "disable"}, ReadOnly: readOnly,
		})
	}
	dbtest.RunContract(t, conn, dbtest.Fixture{
		NS:           db.Namespace{Database: "app", Schema: "app"},
		SleepSQL:     "SELECT pg_sleep(30)",
		OpenReadOnly: func(ctx context.Context) (db.Conn, error) { return open(ctx, true) },
	})
}
