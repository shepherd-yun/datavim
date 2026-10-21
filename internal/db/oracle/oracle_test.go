package oracle

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/sijms/go-ora/v2/network"

	"github.com/shepherd-yun/datavim/internal/db"
)

func TestURL(t *testing.T) {
	cfg := db.ConnConfig{Host: "h", Port: 1521, User: "hr", Password: "p@ss:w/rd?#%", Params: map[string]string{"TIMEOUT": "30"}}
	u, err := url.Parse(connURL(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := u.User.Password(); p != cfg.Password || u.User.Username() != "hr" {
		t.Errorf("user = %v", u.User)
	}
	if u.Path != "/FREEPDB1" || u.Host != "h:1521" {
		t.Errorf("host %q path %q", u.Host, u.Path)
	}
	if u.Query().Get("TIMEOUT") != "30" {
		t.Errorf("query = %v", u.Query())
	}
	cfg.Database = "ORCLPDB"
	if u, _ := url.Parse(connURL(cfg)); u.Path != "/ORCLPDB" {
		t.Errorf("service path = %q", u.Path)
	}
}

func TestTrimSemicolon(t *testing.T) {
	for in, want := range map[string]string{
		"select 1 from dual;":      "select 1 from dual",
		"select 1 from dual ;  \n": "select 1 from dual",
		"begin null; end;":         "begin null; end;",
		"select ';' from dual":     "select ';' from dual",
		"create function f return number is begin return 1; end;\n/\n": "create function f return number is begin return 1; end;",
	} {
		if got, _ := prepare(in); got != want {
			t.Errorf("prepare(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestText(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 500000000, time.FixedZone("", 8*3600))
	for _, tc := range []struct {
		typ  string
		v    any
		want string
	}{
		{"NUMBER", "12.30", "12.30"},
		{"DATE", ts.Truncate(time.Second), "2026-01-02 03:04:05"},
		{"TIMESTAMP", ts, "2026-01-02 03:04:05.5"},
		{"TIMESTAMPTZ", ts, "2026-01-02 03:04:05.5 +08:00"},
		{"RAW", []byte{0x0a, 0xff}, "0AFF"},
		{"OCIBlobLocator", []byte{1}, "01"},
		{"NCHAR", "张三", "张三"},
		{"IBDouble", 1.5, "1.5"},
		{"IBFloat", float32(0.1), "0.1"},
	} {
		if got := text(tc.typ, tc.v); got != tc.want {
			t.Errorf("text(%s, %v) = %q, want %q", tc.typ, tc.v, got, tc.want)
		}
	}
}

func TestDDLWriter(t *testing.T) {
	w := dialect{}.DDL()
	ref := db.TableRef{Namespace: db.Namespace{Schema: "HR"}, Name: "Emp"}
	for got, want := range map[string]string{
		w.DropTable(ref, false):           `DROP TABLE "HR"."Emp"`,
		w.DropTable(ref, true):            `DROP VIEW "HR"."Emp"`,
		w.TruncateTable(ref):              `TRUNCATE TABLE "HR"."Emp"`,
		w.RenameTable(ref, "Emp2", false): `ALTER TABLE "HR"."Emp" RENAME TO "Emp2"`,
		w.RenameTable(ref, "V2", true):    `RENAME "Emp" TO "V2"`,
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if got := (dialect{}).Paginate("select * from t", 10, 20); got != "select * from t OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY" {
		t.Errorf("Paginate = %q", got)
	}
}

func TestWrapKilled(t *testing.T) {
	for _, code := range []int{28, 1012, 3113, 3114} {
		if err := wrap(&network.OracleError{ErrCode: code}); !errors.Is(err, db.ErrConnLost) {
			t.Errorf("ORA-%05d: %v is not ErrConnLost", code, err)
		}
	}
	if err := wrap(&network.OracleError{ErrCode: 942}); errors.Is(err, db.ErrConnLost) {
		t.Errorf("ORA-00942 is ErrConnLost")
	}
}
