package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/meta"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

func TestDefaultNamespace(t *testing.T) {
	tests := []struct {
		driver string
		key    meta.Key
		ns     db.Namespace
		want   db.Namespace
	}{
		{"mysql", meta.Key{Conn: "c"}, db.Namespace{}, db.Namespace{Database: "cfgdb"}},
		{"postgres", meta.Key{Conn: "c", Database: "cfgdb"}, db.Namespace{}, db.Namespace{Database: "cfgdb", Schema: "public"}},
		{"sqlserver", meta.Key{Conn: "c", Database: "cfgdb"}, db.Namespace{}, db.Namespace{Database: "cfgdb", Schema: "dbo"}},
		{"sqlserver", meta.Key{Conn: "c", Database: "crm"}, db.Namespace{Database: "crm", Schema: "sales"}, db.Namespace{Database: "crm", Schema: "sales"}},
		{"oracle", meta.Key{Conn: "c"}, db.Namespace{}, db.Namespace{}},
		{"oracle", meta.Key{Conn: "c"}, db.Namespace{Schema: "HR"}, db.Namespace{Schema: "HR"}},
	}
	for _, tt := range tests {
		cfg := config.Connection{Driver: tt.driver, Database: "cfgdb"}
		if got := defaultNamespace(cfg, tt.key, tt.ns); got != tt.want {
			t.Errorf("%s %+v: got %+v, want %+v", tt.driver, tt.ns, got, tt.want)
		}
	}
}

func TestMetaKeyFor(t *testing.T) {
	b := newFakeBackend()
	b.conns = append(b.conns,
		config.Connection{Name: "ss", Driver: "sqlserver"},
		config.Connection{Name: "ora", Driver: "oracle", Database: "FREEPDB1"},
	)
	tests := []struct {
		conn, database string
		want           meta.Key
	}{
		{"local", "app", meta.Key{Conn: "local"}},
		{"prod", "", meta.Key{Conn: "prod", Database: "postgres"}},
		{"ss", "crm", meta.Key{Conn: "ss", Database: "crm"}},
		{"ss", "", meta.Key{Conn: "ss", Database: "master"}},
		{"ora", "", meta.Key{Conn: "ora"}},
	}
	for _, tt := range tests {
		got, ok := metaKeyFor(b, tt.conn, tt.database)
		if !ok || got != tt.want {
			t.Errorf("metaKeyFor(%s, %q) = %+v, %v; want %+v", tt.conn, tt.database, got, ok, tt.want)
		}
	}
}

// bracketDialect quotes like SQL Server, doubleQuoteDialect like Oracle.
type (
	bracketDialect     struct{ fakeDialect }
	doubleQuoteDialect struct{ fakeDialect }
)

func (bracketDialect) QuoteIdent(s string) string {
	return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
}
func (doubleQuoteDialect) QuoteIdent(s string) string { return db.Quote(s, '"') }

func TestScopeSQL(t *testing.T) {
	tests := []struct {
		d    sqlx.Dialect
		q    db.Dialect
		ns   db.Namespace
		want string
	}{
		{sqlx.MySQL, fakeDialect{}, db.Namespace{Database: "app"}, "USE `app`"},
		{sqlx.Postgres, doubleQuoteDialect{}, db.Namespace{Database: "app", Schema: "s"}, `SET search_path TO "s", public`},
		{sqlx.SQLServer, bracketDialect{}, db.Namespace{Database: "crm", Schema: "dbo"}, "USE [crm]"},
		{sqlx.Oracle, doubleQuoteDialect{}, db.Namespace{Schema: "HR"}, `ALTER SESSION SET CURRENT_SCHEMA = "HR"`},
		{sqlx.Oracle, doubleQuoteDialect{}, db.Namespace{}, ""},
		{sqlx.SQLServer, bracketDialect{}, db.Namespace{}, ""},
	}
	for _, tt := range tests {
		if got := scopeSQL(tt.d, tt.q, tt.ns); got != tt.want {
			t.Errorf("scopeSQL(%s, %+v) = %q, want %q", tt.d.Driver(), tt.ns, got, tt.want)
		}
	}
}

func TestSchemaToolsGate(t *testing.T) {
	b := newFakeBackend()
	b.conns = append(b.conns, config.Connection{Name: "mssql", Driver: "sqlserver", Env: config.EnvDev})
	m := newTestModelWith(t, b)
	b.conn.setSchemas(usersSchema())
	selectNode(t, m, "mssql", "app", "users")
	press(t, m, "e")
	if len(m.tabs) != 0 {
		t.Fatalf("Modify Table opened %d tabs", len(m.tabs))
	}
	if !strings.Contains(statusLine(m), "not supported for sqlserver yet") {
		t.Fatalf("modify: %s", statusLine(m))
	}
	press(t, m, "m")
	if !strings.Contains(statusLine(m), "not supported for sqlserver yet") {
		t.Fatalf("compare: %s", statusLine(m))
	}
	if cmd := m.importFromNode(); cmd != nil || !strings.Contains(statusLine(m), "not supported for sqlserver yet") {
		t.Fatalf("import: %s", statusLine(m))
	}
}

func TestTablePartsBySchemas(t *testing.T) {
	ref := db.TableRef{Namespace: db.Namespace{Database: "crm", Schema: "dbo"}, Name: "t"}
	if got := strings.Join(tableParts(sqlx.SQLServer, ref), "."); got != "dbo.t" {
		t.Errorf("sqlserver: %s", got)
	}
	if got := strings.Join(tableParts(sqlx.MySQL, db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "t"}), "."); got != "app.t" {
		t.Errorf("mysql: %s", got)
	}
}

func TestTreePathsWithoutDatabase(t *testing.T) {
	ref := db.TableRef{Namespace: db.Namespace{Schema: "HR"}, Name: "EMP"}
	if got := strings.Join(tablePath(ref.Namespace, ref.Name), "/"); got != "HR/EMP" {
		t.Errorf("tablePath = %s", got)
	}
	if got := strings.Join(nsPath(ref.Namespace), "/"); got != "HR" {
		t.Errorf("nsPath = %s", got)
	}
	if got := strings.Join(tablePath(db.Namespace{Database: "app"}, "t"), "/"); got != "app/t" {
		t.Errorf("mysql tablePath = %s", got)
	}
}
