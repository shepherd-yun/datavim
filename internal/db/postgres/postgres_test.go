package postgres

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/shepherd-yun/datavim/internal/db"
)

func TestConnStringKeepsSpecialCharacters(t *testing.T) {
	cs := connString(db.ConnConfig{
		Host: "db.local", Port: 6543, User: "app", Password: "p@ss:w/rd?#%",
		Params: map[string]string{"sslmode": "disable", "application_name": "datavim"},
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
	if cfg.RuntimeParams["application_name"] != "datavim" {
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

func TestPoolConfig(t *testing.T) {
	pc, err := poolConfig(db.ConnConfig{Host: "h", Port: 5432, User: "u", Database: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if pc.ConnConfig.BuildContextWatcherHandler == nil {
		t.Fatal("cancelling a context must send a cancel request")
	}
	if _, ok := pc.ConnConfig.RuntimeParams["default_transaction_read_only"]; ok {
		t.Fatal("read-write connections must not set default_transaction_read_only")
	}
	ro, err := poolConfig(db.ConnConfig{Host: "h", Port: 5432, User: "u", Database: "app", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := ro.ConnConfig.RuntimeParams["default_transaction_read_only"]; got != "on" {
		t.Fatalf("default_transaction_read_only = %q", got)
	}
}

func TestPoolConfigDialsThroughTunnel(t *testing.T) {
	var dialed []string
	dial := func(_ context.Context, network, addr string) (net.Conn, error) {
		dialed = append(dialed, network+" "+addr)
		return nil, errors.New("no network in unit tests")
	}
	pcfg, err := poolConfig(db.ConnConfig{Host: "db.internal", Port: 5432, User: "u", Dial: dial})
	if err != nil {
		t.Fatal(err)
	}
	cc := pcfg.ConnConfig
	addrs, err := cc.LookupFunc(context.Background(), "db.internal")
	if err != nil || !slices.Equal(addrs, []string{"db.internal"}) {
		t.Fatalf("lookup = %v, %v; names must be resolved on the far side of the tunnel", addrs, err)
	}
	if _, err := cc.DialFunc(context.Background(), "tcp", "db.internal:5432"); err == nil {
		t.Fatal("dial error is passed through")
	}
	if !slices.Equal(dialed, []string{"tcp db.internal:5432"}) {
		t.Fatalf("dialed = %v", dialed)
	}
}

func TestLocate(t *testing.T) {
	sql := "SELECT 'é', nope FROM t"
	pgErr := &pgconn.PgError{Message: `column "nope" does not exist`, Code: "42703", Position: 13}
	err := locate(pgErr, sql)
	off, ok := db.ErrorOffset(err)
	if !ok || !strings.HasPrefix(sql[off:], "nope") {
		t.Fatalf("offset = %d, %v", off, ok)
	}
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || err.Error() != pgErr.Error() {
		t.Fatal("the PgError stays reachable and the text is unchanged")
	}
	if _, ok := db.ErrorOffset(locate(&pgconn.PgError{Message: "x"}, sql)); ok {
		t.Fatal("no position, no offset")
	}
	if locate(nil, sql) != nil {
		t.Fatal("nil stays nil")
	}
}

func TestDDLWriter(t *testing.T) {
	w := dialect{}.DDL()
	users := db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "users"}
	odd := db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: `we"ird name`}
	for _, c := range []struct{ got, want string }{
		{w.DropTable(users, false), `DROP TABLE "public"."users"`},
		{w.DropTable(users, true), `DROP VIEW "public"."users"`},
		{w.RenameTable(users, "people", false), `ALTER TABLE "public"."users" RENAME TO "people"`},
		{w.RenameTable(users, "people", true), `ALTER VIEW "public"."users" RENAME TO "people"`},
		{w.TruncateTable(users), `TRUNCATE TABLE "public"."users"`},
		{w.DropTable(odd, false), `DROP TABLE "public"."we""ird name"`},
		{w.RenameTable(users, "Mixed Case", false), `ALTER TABLE "public"."users" RENAME TO "Mixed Case"`},
	} {
		if c.got != c.want {
			t.Errorf("got  %s\nwant %s", c.got, c.want)
		}
	}
}

func TestNormType(t *testing.T) {
	for in, want := range map[string]string{
		"character varying(100)":         "varchar(100)",
		"character varying":              "varchar",
		"character varying(10)[]":        "varchar(10)[]",
		"character(1)":                   "char(1)",
		"integer":                        "int",
		"integer[]":                      "int[]",
		"boolean":                        "bool",
		"timestamp without time zone":    "timestamp",
		"timestamp(3) without time zone": "timestamp(3)",
		"timestamp with time zone":       "timestamptz",
		"time with time zone":            "timetz",
		"bigint":                         "bigint",
		"numeric(10,2)":                  "numeric(10,2)",
		"double precision":               "double precision",
		"text":                           "text",
	} {
		if got := normType(in); got != want {
			t.Errorf("normType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormDefault(t *testing.T) {
	s := func(v string) *string { return &v }
	show := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	for _, c := range []struct{ in, want *string }{
		{nil, nil},
		{s("0"), s("0")},
		{s("'x'::character varying"), s("'x'")},
		{s("'it''s'::text"), s("'it''s'")},
		{s("'{}'::integer[]"), s("'{}'")},
		{s("'2024-01-01'::date"), s("'2024-01-01'")},
		{s("1.5::numeric(10,2)"), s("1.5")},
		{s("NULL::character varying"), nil},
		{s("CURRENT_TIMESTAMP"), s("CURRENT_TIMESTAMP")},
		{s("nextval('users_id_seq'::regclass)"), s("nextval('users_id_seq'::regclass)")},
		{s("(now() + '1 day'::interval)"), s("(now() + '1 day'::interval)")},
	} {
		if got := normDefault(c.in); show(got) != show(c.want) {
			t.Errorf("normDefault(%s) = %s, want %s", show(c.in), show(got), show(c.want))
		}
	}
}

func TestCreateTable(t *testing.T) {
	w := dialect{}.DDL()
	x := "'x'"
	s := db.TableSchema{
		Ref: db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "Audit"},
		Columns: []db.Column{
			{Name: "id", Type: "bigint", AutoIncrement: true},
			{Name: "code", Type: "varchar(10)", Nullable: true, Default: &x, Collation: "C", Comment: "it's"},
			{Name: "total", Type: "int", Nullable: true, Generated: "id * 2"},
		},
		Indexes: []db.Index{
			{Name: "audit_pkey", Columns: []string{"id"}, Unique: true, Primary: true, Constraint: true, Method: "btree"},
			{Name: "audit_code_key", Columns: []string{"code"}, Unique: true, Constraint: true, Method: "btree"},
			{Name: "audit_lower", Columns: []string{"lower((code)::text)"}, Method: "btree", Where: "id > 0"},
			{Name: "audit_gin", Columns: []string{"code"}, Method: "gin"},
		},
		Comment: "who did what",
	}
	stmts, manual := w.CreateTable(s)
	want := []string{
		`CREATE TABLE "public"."Audit" (` + "\n" +
			`  "id" bigint GENERATED BY DEFAULT AS IDENTITY NOT NULL,` + "\n" +
			`  "code" varchar(10) COLLATE "C" DEFAULT 'x',` + "\n" +
			`  "total" int GENERATED ALWAYS AS (id * 2) STORED,` + "\n" +
			`  CONSTRAINT "audit_pkey" PRIMARY KEY (id),` + "\n" +
			`  CONSTRAINT "audit_code_key" UNIQUE (code)` + "\n" +
			`)`,
		`CREATE INDEX "audit_lower" ON "public"."Audit" (lower((code)::text)) WHERE id > 0`,
		`CREATE INDEX "audit_gin" ON "public"."Audit" USING gin (code)`,
		`COMMENT ON TABLE "public"."Audit" IS 'who did what'`,
		`COMMENT ON COLUMN "public"."Audit"."code" IS 'it''s'`,
	}
	if diff := cmp.Diff(want, stmts); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if len(manual) != 0 {
		t.Errorf("manual = %+v", manual)
	}
}

func TestAlterTable(t *testing.T) {
	w := dialect{}.DDL()
	users := db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "users"}
	x := "'x'"
	changes := []db.SchemaChange{
		{Kind: db.DropIndex, OldIndex: &db.Index{Name: "users_pkey", Unique: true, Primary: true, Constraint: true}},
		{Kind: db.DropIndex, OldIndex: &db.Index{Name: "users_name_idx"}},
		{Kind: db.RenameColumn, Old: &db.Column{Name: "nm"}, New: &db.Column{Name: "Nick"}},
		{Kind: db.DropColumn, Old: &db.Column{Name: "legacy"}},
		{Kind: db.AddColumn, New: &db.Column{Name: "email", Type: "varchar(255)", Nullable: true, Comment: "mail"}, After: "name"},
		{
			Kind: db.AlterColumn, Old: &db.Column{Name: "name", Type: "varchar(50)", Nullable: true},
			New: &db.Column{Name: "name", Type: "varchar(100)", Default: &x}, Attrs: db.AttrType | db.AttrNullable | db.AttrDefault,
		},
		{
			Kind: db.AlterColumn, Old: &db.Column{Name: "note", Type: "text", Comment: "c", Default: &x},
			New: &db.Column{Name: "note", Type: "text", Collation: "C"}, Attrs: db.AttrComment | db.AttrCollation | db.AttrDefault,
		},
		{Kind: db.AlterColumn, Old: &db.Column{Name: "price", Type: "text"}, New: &db.Column{Name: "price", Type: "int"}, Attrs: db.AttrType},
		{Kind: db.AlterColumn, Old: &db.Column{Name: "n", Type: "int"}, New: &db.Column{Name: "n", Type: "int", AutoIncrement: true}, Attrs: db.AttrAutoIncrement},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "users_pkey", Columns: []string{"id", "tenant"}, Unique: true, Primary: true, Constraint: true}},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "users_email_key", Columns: []string{"email"}, Unique: true, Constraint: true}},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "users_lower", Columns: []string{"lower(email)"}, Unique: true, Method: "btree", Where: "email IS NOT NULL"}},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "users_tags", Columns: []string{"tags"}, Method: "gin"}},
		{Kind: db.AlterTableComment, OldValue: "people", NewValue: ""},
		{Kind: db.AlterTableOption, Option: "engine", NewValue: "InnoDB"},
	}
	stmts, manual := w.AlterTable(users, changes)
	want := []string{
		`ALTER TABLE "public"."users" DROP CONSTRAINT "users_pkey"`,
		`DROP INDEX "public"."users_name_idx"`,
		`ALTER TABLE "public"."users" RENAME COLUMN "nm" TO "Nick"`,
		`ALTER TABLE "public"."users" DROP COLUMN "legacy"`,
		`ALTER TABLE "public"."users" ADD COLUMN "email" varchar(255)`,
		`COMMENT ON COLUMN "public"."users"."email" IS 'mail'`,
		`ALTER TABLE "public"."users" ALTER COLUMN "name" TYPE varchar(100), ALTER COLUMN "name" SET NOT NULL, ALTER COLUMN "name" SET DEFAULT 'x'`,
		`ALTER TABLE "public"."users" ALTER COLUMN "note" TYPE text COLLATE "C", ALTER COLUMN "note" DROP DEFAULT`,
		`COMMENT ON COLUMN "public"."users"."note" IS NULL`,
		`ALTER TABLE "public"."users" ADD CONSTRAINT "users_pkey" PRIMARY KEY (id, tenant)`,
		`ALTER TABLE "public"."users" ADD CONSTRAINT "users_email_key" UNIQUE (email)`,
		`CREATE UNIQUE INDEX "users_lower" ON "public"."users" (lower(email)) WHERE email IS NOT NULL`,
		`CREATE INDEX "users_tags" ON "public"."users" USING gin (tags)`,
		`COMMENT ON TABLE "public"."users" IS NULL`,
	}
	if diff := cmp.Diff(want, stmts); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	var kinds []string
	for _, c := range manual {
		kinds = append(kinds, c.Kind.String()+" "+c.Option)
	}
	if diff := cmp.Diff([]string{"AlterColumn ", "AlterColumn ", "AlterTableOption engine"}, kinds); diff != "" {
		t.Errorf("manual (-want +got):\n%s", diff)
	}
}

func TestCastable(t *testing.T) {
	for _, c := range []struct {
		old, nw string
		want    bool
	}{
		{"varchar(50)", "varchar(100)", true},
		{"int", "bigint", true},
		{"bigint", "int", true},
		{"int", "numeric(10,2)", true},
		{"int", "text", true},
		{"uuid", "text", true},
		{"int[]", "text", true},
		{"int[]", "bigint[]", true},
		{"date", "timestamptz", true},
		{"text", "int", false},
		{"text", "uuid", false},
		{"bool", "int", false},
		{"int", "int[]", false},
	} {
		if got := castable(c.old, c.nw); got != c.want {
			t.Errorf("castable(%q, %q) = %v, want %v", c.old, c.nw, got, c.want)
		}
	}
}

func TestNormalizeTypeIsTableSchemas(t *testing.T) {
	w := dialect{}.DDL()
	for in, want := range map[string]string{
		"INTEGER":                  "int",
		"character varying(20)":    "varchar(20)",
		"TIMESTAMP WITH TIME ZONE": "timestamptz",
		"text[]":                   "text[]",
	} {
		if got := w.NormalizeType(in); got != want {
			t.Errorf("NormalizeType(%q) = %q, want %q", in, got, want)
		}
	}
}
