package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func TestConnStringKeepsSpecialCharacters(t *testing.T) {
	cs := connString(db.ConnConfig{
		Host: "db.local", Port: 6543, User: "app", Password: "p@ss:w/rd?#%",
		Params: map[string]string{"sslmode": "disable", "application_name": "sqlvim"},
	})
	cfg, err := pgx.ParseConfig(cs)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != "p@ss:w/rd?#%" || cfg.User != "app" {
		t.Errorf("user/password = %q/%q", cfg.User, cfg.Password)
	}
	if cfg.Host != "db.local" || cfg.Port != 6543 {
		t.Errorf("host/port = %s/%d", cfg.Host, cfg.Port)
	}
	if cfg.Database != "postgres" {
		t.Errorf("database = %q, want default postgres", cfg.Database)
	}
	if cfg.RuntimeParams["application_name"] != "sqlvim" {
		t.Errorf("runtime params = %v", cfg.RuntimeParams)
	}
	if cfg.ConnectTimeout != 10*time.Second {
		t.Errorf("connect timeout = %v", cfg.ConnectTimeout)
	}
}

func TestQualifiedName(t *testing.T) {
	d := dialect{}
	got := d.QualifiedName(db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "app"}, Name: "MixedCase"})
	if got != `"app"."MixedCase"` {
		t.Fatalf("got %s", got)
	}
}

func TestBuildTableDDL(t *testing.T) {
	def := "'hi'::text"
	got := buildTableDDL(`"app"."users"`,
		[]db.Column{
			{Name: "id", Type: "bigint", PrimaryKey: true},
			{Name: "note", Type: "text", Nullable: true, Default: &def},
		},
		[]constraint{{name: "users_pkey", def: "PRIMARY KEY (id)"}},
		[]string{"CREATE UNIQUE INDEX users_email_key ON app.users USING btree (email)"},
	)
	want := `CREATE TABLE "app"."users" (
    "id" bigint NOT NULL,
    "note" text DEFAULT 'hi'::text,
    CONSTRAINT "users_pkey" PRIMARY KEY (id)
);
CREATE UNIQUE INDEX users_email_key ON app.users USING btree (email);
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if !strings.HasPrefix(got, "CREATE TABLE") {
		t.Fatal("must start with CREATE TABLE")
	}
}
