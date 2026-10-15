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
		Driver:   "mysql",
		NS:       db.Namespace{Database: "app"},
		SleepSQL: "SELECT SLEEP(30)",
		ManyRowsSQL: "WITH d AS (SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 " +
			"UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) " +
			"SELECT a.n FROM d a, d b, d c, d e, d f, d g, d h, d i",
		OpenReadOnly: func(ctx context.Context) (db.Conn, error) {
			return mysql.Driver{}.Open(ctx, db.ConnConfig{
				Host: host, Port: port, User: "root", Password: password, Database: "app", ReadOnly: true,
			})
		},
		NormTypesSQL: []string{
			"CREATE TABLE norm_types (id INT AUTO_INCREMENT PRIMARY KEY, n INT(11) NOT NULL DEFAULT 0, " +
				"flag TINYINT(1), name VARCHAR(20) DEFAULT 'x', created_at TIMESTAMP NULL DEFAULT CURRENT_TIMESTAMP, " +
				"big BIGINT UNSIGNED, price DECIMAL(10,2), body TEXT, ci VARCHAR(10) COLLATE utf8mb4_bin) " +
				"COMMENT 'normalized'",
		},
		NormTypes: map[string]string{
			"id": "int", "n": "int", "flag": "tinyint(1)", "name": "varchar(20)", "created_at": "timestamp",
			"big": "bigint unsigned", "price": "decimal(10,2)", "body": "text", "ci": "varchar(10)",
		},
		AutoIncrement: []string{"id"},
		CICollation:   "utf8mb4_bin",
		CompareSQL: []string{
			"CREATE TABLE cmp_src (id BIGINT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(100) NOT NULL COMMENT 'who', " +
				"email VARCHAR(255), qty BIGINT NOT NULL DEFAULT 1, `Odd Col` VARCHAR(10), " +
				"created TIMESTAMP NULL DEFAULT CURRENT_TIMESTAMP, updated TIMESTAMP NULL ON UPDATE CURRENT_TIMESTAMP, " +
				"KEY cmp_src_name (name), " +
				"UNIQUE KEY cmp_src_email (email)) COMMENT 'it''s the source'",
			"CREATE TABLE cmp_dst (id BIGINT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(50) NULL, " +
				"qty INT NOT NULL DEFAULT 0, updated TIMESTAMP NULL, legacy INT, KEY cmp_legacy (legacy), KEY cmp_dst_name (name, qty))",
		},
		DataCompareSQL: []string{
			"CREATE TABLE dat_src (id BIGINT PRIMARY KEY, name VARCHAR(50) NOT NULL, note TEXT, price DECIMAL(10,2), seen_at DATETIME)",
			"CREATE TABLE dat_dst (id BIGINT PRIMARY KEY, name VARCHAR(50) NOT NULL, note TEXT, price DECIMAL(10,2), seen_at DATETIME)",
			`INSERT INTO dat_src VALUES (1, 'same', 'x', 1.50, '2026-01-02 03:04:05'), (2, '李雷', '', NULL, NULL),
				(3, 'it''s', 'back\\slash', 2.00, '2026-01-02 03:04:05'), (5, 'new', NULL, 9.99, NULL)`,
			`INSERT INTO dat_dst VALUES (1, 'same', 'x', 1.50, '2026-01-02 03:04:05'), (2, '李雷', NULL, NULL, NULL),
				(3, 'its', 'back\\slash', 2.00, '2026-01-02 03:04:06'), (4, 'gone', NULL, NULL, NULL)`,
		},
	})
	// ON UPDATE is MySQL's own: TableSchema reads it, and the synced and
	// created tables keep it.
	for _, name := range []string{"cmp_src", "cmp_dst", "cmp_new"} {
		s, err := conn.Meta().TableSchema(ctx, db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range s.Columns {
			if c.Name == "updated" && c.OnUpdate != "CURRENT_TIMESTAMP" {
				t.Errorf("%s.updated: OnUpdate = %q, want CURRENT_TIMESTAMP", name, c.OnUpdate)
			}
		}
	}
}
