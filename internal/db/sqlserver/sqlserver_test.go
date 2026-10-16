package sqlserver

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func TestDSN(t *testing.T) {
	cfg := db.ConnConfig{
		Host: "10.0.0.5", Port: 1433, User: "sa", Password: "p@ss:w/rd?#%", Database: "crm_MSCRM",
		Params: map[string]string{"encrypt": "disable", "tlsmin": "1.0", "TrustServerCertificate": "true"},
	}
	u, err := url.Parse(dsn(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := u.User.Password(); p != cfg.Password || u.User.Username() != "sa" {
		t.Errorf("user info = %v", u.User)
	}
	if u.Host != "10.0.0.5:1433" || u.Path != "" {
		t.Errorf("host %q path %q", u.Host, u.Path)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"database": "crm_MSCRM", "encrypt": "disable", "tlsmin": "1.0", "TrustServerCertificate": "true",
		"dial timeout": "10", "app name": "sqlvim",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if _, err := mssql.NewConnector(dsn(cfg)); err != nil {
		t.Errorf("the driver rejects the DSN: %v", err)
	}
	cfg.Database, cfg.Params = "", nil
	if got := mustQuery(t, dsn(cfg)).Get("database"); got != "master" {
		t.Errorf("empty database: %q", got)
	}
}

func mustQuery(t *testing.T, s string) url.Values {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestOpenErrorsHideTheDSN(t *testing.T) {
	err := hideDSN(errors.New(`bad "sqlserver://sa:secret@h:1/?database=x"`), "sqlserver://sa:secret@h:1/?database=x")
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaks the password: %v", err)
	}
}

func TestPaginateSQLServer(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"SELECT * FROM t", "SELECT * FROM t ORDER BY (SELECT NULL) OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY"},
		{"SELECT * FROM t ORDER BY id", "SELECT * FROM t ORDER BY id OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY"},
		{"SELECT * FROM (SELECT TOP 5 * FROM t ORDER BY id) q", "SELECT * FROM (SELECT TOP 5 * FROM t ORDER BY id) q ORDER BY (SELECT NULL) OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY"},
		{"SELECT 'order by' FROM t", "SELECT 'order by' FROM t ORDER BY (SELECT NULL) OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY"},
	} {
		if got := (dialect{}).Paginate(tc.in, 10, 20); got != tc.want {
			t.Errorf("Paginate(%q) =\n %q\nwant\n %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatGUID(t *testing.T) {
	// SQL Server sends the first three groups little-endian.
	wire := []byte{0x33, 0x22, 0x11, 0x00, 0x55, 0x44, 0x77, 0x66, 0x88, 0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	if got := text("UNIQUEIDENTIFIER", wire); got != "00112233-4455-6677-8899-AABBCCDDEEFF" {
		t.Fatalf("GUID = %s", got)
	}
}

func TestText(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 120000000, time.FixedZone("", 8*3600))
	for _, tc := range []struct {
		typ  string
		v    any
		want string
	}{
		{"INT", int64(42), "42"},
		{"BIT", true, "1"},
		{"BIT", false, "0"},
		{"FLOAT", 1.5, "1.5"},
		{"DECIMAL", []byte("12.30"), "12.30"},
		{"MONEY", []byte("1.5000"), "1.5000"},
		{"NVARCHAR", "张三", "张三"},
		{"VARBINARY", []byte{0x0a, 0xff}, "0x0AFF"},
		{"DATE", ts, "2026-01-02"},
		{"TIME", ts, "03:04:05.12"},
		{"DATETIME2", ts, "2026-01-02 03:04:05.12"},
		{"DATETIME", ts.Truncate(time.Second), "2026-01-02 03:04:05"},
		{"DATETIMEOFFSET", ts, "2026-01-02 03:04:05.12 +08:00"},
	} {
		if got := text(tc.typ, tc.v); got != tc.want {
			t.Errorf("text(%s, %v) = %q, want %q", tc.typ, tc.v, got, tc.want)
		}
	}
}

func TestTypeName(t *testing.T) {
	for _, tc := range []struct {
		name                string
		maxLen, prec, scale int
		want                string
	}{
		{"int", 4, 10, 0, "int"},
		{"nvarchar", 100, 0, 0, "nvarchar(50)"},
		{"nvarchar", -1, 0, 0, "nvarchar(max)"},
		{"varchar", 20, 0, 0, "varchar(20)"},
		{"varbinary", -1, 0, 0, "varbinary(max)"},
		{"decimal", 9, 10, 2, "decimal(10,2)"},
		{"datetime2", 8, 27, 7, "datetime2(7)"},
		{"datetime", 8, 23, 3, "datetime"},
	} {
		if got := typeName(tc.name, tc.maxLen, tc.prec, tc.scale); got != tc.want {
			t.Errorf("typeName(%s, %d, %d, %d) = %q, want %q", tc.name, tc.maxLen, tc.prec, tc.scale, got, tc.want)
		}
	}
}

func TestLocate(t *testing.T) {
	sql := "SELECT *\nFORM [dbo].[users]"
	err := locate(mssql.Error{Number: 102, LineNo: 2, Message: "Incorrect syntax near 'FORM'."}, sql)
	off, ok := db.ErrorOffset(err)
	if !ok || !strings.HasPrefix(sql[off:], "FORM") {
		t.Fatalf("offset %d, %v", off, ok)
	}
	err = locate(mssql.Error{Number: 102, LineNo: 1, Message: "Incorrect syntax near ')'."}, "SELECT (1))")
	if off, ok := db.ErrorOffset(err); !ok || off != 9 {
		t.Fatalf("near ')': offset %d, %v", off, ok)
	}
}

func TestDDLWriter(t *testing.T) {
	w := dialect{}.DDL()
	ref := db.TableRef{Namespace: db.Namespace{Database: "crm", Schema: "dbo"}, Name: "it's]"}
	for got, want := range map[string]string{
		w.DropTable(ref, false):            "DROP TABLE [dbo].[it's]]]",
		w.DropTable(ref, true):             "DROP VIEW [dbo].[it's]]]",
		w.TruncateTable(ref):               "TRUNCATE TABLE [dbo].[it's]]]",
		w.RenameTable(ref, "new's", false): "EXEC sp_rename N'[dbo].[it''s]]]', N'new''s'",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if stmts, manual := w.AlterTable(ref, []db.SchemaChange{{Kind: db.AddColumn}}); len(stmts) != 0 || len(manual) != 1 {
		t.Errorf("AlterTable = %v, %v", stmts, manual)
	}
}
