//go:build integration

package mysql_test

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/db/dbtest"
	"github.com/shepherd-yun/sqlvim/internal/db/mysql"
)

var fixture = []string{
	"CREATE TABLE users (id BIGINT PRIMARY KEY, name VARCHAR(100) NOT NULL COMMENT 'display name', " +
		"email VARCHAR(100), note VARCHAR(100) DEFAULT 'hi', UNIQUE KEY users_email_key (email))",
	"CREATE TABLE orders (id BIGINT PRIMARY KEY, user_id BIGINT NOT NULL, " +
		"CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES users (id))",
	"CREATE TABLE `order` (id INT)",
	"CREATE TABLE `MixedCase` (id INT)",
	"CREATE VIEW active_users AS SELECT id, name FROM users",
	"CREATE FUNCTION add_one(x INT) RETURNS INT DETERMINISTIC RETURN x + 1",
	"INSERT INTO users VALUES (1, 'alice', 'a@x.com', DEFAULT), (2, '李雷', '', NULL), (3, 'bob', NULL, 'multi\\nline')",
}

func TestContract(t *testing.T) {
	ctx := context.Background()
	const password = "p@ss:w/rd?#%"
	ctr, err := tcmysql.Run(ctx, "mysql:8.4",
		tcmysql.WithDatabase("app"),
		tcmysql.WithUsername("root"),
		tcmysql.WithPassword(password),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "3306/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, _ := net.SplitHostPort(endpoint)
	port, _ := strconv.Atoi(portStr)

	conn, err := mysql.Driver{}.Open(ctx, db.ConnConfig{
		Host: host, Port: port, User: "root", Password: password, Database: "app",
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
		NS:       db.Namespace{Database: "app"},
		SleepSQL: "SELECT SLEEP(30)",
		OpenReadOnly: func(ctx context.Context) (db.Conn, error) {
			return mysql.Driver{}.Open(ctx, db.ConnConfig{
				Host: host, Port: port, User: "root", Password: password, Database: "app", ReadOnly: true,
			})
		},
	})
}
