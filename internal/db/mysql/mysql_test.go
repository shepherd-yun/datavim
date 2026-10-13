package mysql

import (
	"testing"

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
