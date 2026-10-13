package mysql

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func TestDSNConfigKeepsSpecialCharacters(t *testing.T) {
	mc, err := dsnConfig(db.ConnConfig{
		Host: "db.local", Port: 3307, User: "app", Password: "p@ss:w/rd?#%", Database: "shop",
		Params: map[string]string{"time_zone": "'+00:00'"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if mc.Passwd != "p@ss:w/rd?#%" || mc.User != "app" {
		t.Errorf("user/password = %q/%q", mc.User, mc.Passwd)
	}
	if mc.Addr != "db.local:3307" || mc.DBName != "shop" {
		t.Errorf("addr/db = %q/%q", mc.Addr, mc.DBName)
	}
	if !mc.InterpolateParams {
		t.Error("InterpolateParams must be on so every query uses the text protocol")
	}
	if mc.Params["time_zone"] != "'+00:00'" {
		t.Errorf("params = %v", mc.Params)
	}
}

func TestDisplayText(t *testing.T) {
	if got := displayText("李雷"); got != "李雷" {
		t.Errorf("utf8 = %q", got)
	}
	if got := displayText(string([]byte{0xde, 0xad, 0xbe, 0xef})); got != "0xDEADBEEF" {
		t.Errorf("binary = %q", got)
	}
}

func TestQualifiedName(t *testing.T) {
	got := dialect{}.QualifiedName(db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "order"})
	if got != "`app`.`order`" {
		t.Fatalf("got %s", got)
	}
}

func TestDSNConfigReadOnly(t *testing.T) {
	for _, params := range []map[string]string{nil, {"charset": "utf8mb4"}} {
		mc, err := dsnConfig(db.ConnConfig{Host: "h", Port: 3306, User: "u", ReadOnly: true, Params: params})
		if err != nil {
			t.Fatal(err)
		}
		if mc.Params["transaction_read_only"] != "1" {
			t.Errorf("params=%v: got %v", params, mc.Params)
		}
	}
	mc, err := dsnConfig(db.ConnConfig{Host: "h", Port: 3306, User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mc.Params["transaction_read_only"]; ok {
		t.Error("read-write connections must not set transaction_read_only")
	}
}

func TestDSNConfigDialsThroughTunnel(t *testing.T) {
	var dialed []string
	dial := func(_ context.Context, network, addr string) (net.Conn, error) {
		dialed = append(dialed, network+" "+addr)
		return nil, errors.New("no network in unit tests")
	}
	for _, params := range []map[string]string{nil, {"charset": "utf8mb4"}} {
		mc, err := dsnConfig(db.ConnConfig{Host: "db.internal", Port: 3306, User: "u", Params: params, Dial: dial})
		if err != nil {
			t.Fatal(err)
		}
		if mc.DialFunc == nil {
			t.Fatalf("params=%v: DialFunc not set", params)
		}
		_, _ = mc.DialFunc(context.Background(), mc.Net, mc.Addr)
	}
	if len(dialed) != 2 || dialed[0] != "tcp db.internal:3306" {
		t.Fatalf("dialed = %v", dialed)
	}
	mc, err := dsnConfig(db.ConnConfig{Host: "h", Port: 3306, User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if mc.DialFunc != nil {
		t.Error("direct connections keep the driver's dialer")
	}
}

func syntaxErr(near string, line int) error {
	return &gomysql.MySQLError{Number: 1064, Message: "You have an error in your SQL syntax; " +
		"check the manual that corresponds to your MySQL server version for the right syntax to use near '" +
		near + "' at line " + strconv.Itoa(line)}
}

func TestLocate(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		err       error
		at        string // the text the offset points at; "" means the end
	}{
		{"first line", "SELECT * FORM users", syntaxErr("FORM users", 1), "FORM users"},
		{"later line", "SELECT 1,\n  2 FROM t\nWHERE x", syntaxErr("WHERE x", 3), "WHERE x"},
		{"end of input", "SELECT * FROM", syntaxErr("", 1), ""},
		{"quote inside near", "SELECT 'it''s' x y", syntaxErr("y", 1), "y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := len(tc.sql)
			if tc.at != "" {
				want = strings.Index(tc.sql, tc.at)
			}
			err := locate(tc.err, tc.sql)
			if off, ok := db.ErrorOffset(err); !ok || off != want {
				t.Fatalf("offset = %d, %v; want %d", off, ok, want)
			}
			var me *gomysql.MySQLError
			if !errors.As(err, &me) || err.Error() != tc.err.Error() {
				t.Fatal("the MySQLError stays reachable and the text is unchanged")
			}
		})
	}
	unknown := &gomysql.MySQLError{Number: 1054, Message: "Unknown column 'nope' in 'field list'"}
	if _, ok := db.ErrorOffset(locate(unknown, "SELECT nope")); ok {
		t.Fatal("errors without a position get no offset")
	}
	if locate(nil, "x") != nil {
		t.Fatal("nil stays nil")
	}
}

func TestDSNConfigCountsMatchedRows(t *testing.T) {
	for _, params := range []map[string]string{nil, {"clientFoundRows": "false"}} {
		mc, err := dsnConfig(db.ConnConfig{Host: "h", Port: 3306, User: "u", Params: params})
		if err != nil {
			t.Fatal(err)
		}
		if !mc.ClientFoundRows {
			t.Errorf("params=%v: UPDATE must report matched rows, data edits rely on it", params)
		}
	}
}
