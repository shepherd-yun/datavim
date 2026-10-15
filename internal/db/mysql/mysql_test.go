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

func TestDDLWriter(t *testing.T) {
	w := dialect{}.DDL()
	users := db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"}
	odd := db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "we`ird name"}
	for _, c := range []struct{ got, want string }{
		{w.DropTable(users, false), "DROP TABLE `app`.`users`"},
		{w.DropTable(users, true), "DROP VIEW `app`.`users`"},
		{w.RenameTable(users, "people", false), "RENAME TABLE `app`.`users` TO `app`.`people`"},
		{w.RenameTable(users, "people", true), "RENAME TABLE `app`.`users` TO `app`.`people`"},
		{w.TruncateTable(users), "TRUNCATE TABLE `app`.`users`"},
		{w.DropTable(odd, false), "DROP TABLE `app`.`we``ird name`"},
		{w.RenameTable(users, "Mixed Case", false), "RENAME TABLE `app`.`users` TO `app`.`Mixed Case`"},
	} {
		if c.got != c.want {
			t.Errorf("got  %s\nwant %s", c.got, c.want)
		}
	}
}

func TestNormType(t *testing.T) {
	for in, want := range map[string]string{
		"int(11)":             "int",
		"INT":                 "int",
		"bigint(20) unsigned": "bigint unsigned",
		"tinyint(1)":          "tinyint(1)",
		"tinyint(4)":          "tinyint",
		"varchar(100)":        "varchar(100)",
		"decimal(10,2)":       "decimal(10,2)",
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
	for _, c := range []struct {
		def        *string
		typ, extra string
		want       *string
	}{
		{nil, "int", "", nil},
		{s("0"), "int", "", s("0")},
		{s("1.5"), "decimal(10,2)", "", s("1.5")},
		{s("hi"), "varchar(10)", "", s("'hi'")},
		{s(`it's \`), "varchar(10)", "", s(`'it''s \\'`)},
		{s("1"), "varchar(10)", "", s("'1'")},
		{s("CURRENT_TIMESTAMP"), "timestamp", "default_generated", s("CURRENT_TIMESTAMP")},
		{s("CURRENT_TIMESTAMP(3)"), "datetime(3)", "default_generated on update current_timestamp(3)", s("CURRENT_TIMESTAMP(3)")},
		{s("uuid()"), "varchar(36)", "default_generated", s("(uuid())")},
		{s("b'1'"), "bit(1)", "", s("b'1'")},
	} {
		if got := normDefault(c.def, c.typ, c.extra); show(got) != show(c.want) {
			t.Errorf("normDefault(%s, %q, %q) = %s, want %s", show(c.def), c.typ, c.extra, show(got), show(c.want))
		}
	}
}

func TestCreateTable(t *testing.T) {
	w := dialect{}.DDL()
	zero := "0"
	s := db.TableSchema{
		Ref: db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "audit log"},
		Columns: []db.Column{
			{Name: "id", Type: "bigint", AutoIncrement: true},
			{Name: "n", Type: "int", Default: &zero, Comment: `it's \ here`},
			{Name: "code", Type: "varchar(10)", Nullable: true, Collation: "utf8mb4_bin"},
			{Name: "total", Type: "int", Nullable: true, Generated: "`n` * 2"},
		},
		Indexes: []db.Index{
			{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true, Method: "btree"},
			{Name: "code_u", Columns: []string{"code"}, Unique: true, Method: "btree"},
			{Name: "n_idx", Columns: []string{"n", "code"}, Method: "btree"},
			{Name: "fx", Columns: []string{"(expression)"}, Method: "btree"},
		},
		Comment: "who did what",
		Options: map[string]string{"engine": "InnoDB", "collation": "utf8mb4_0900_ai_ci"},
	}
	stmts, manual := w.CreateTable(s)
	want := "CREATE TABLE `app`.`audit log` (\n" +
		"  `id` bigint NOT NULL AUTO_INCREMENT,\n" +
		"  `n` int NOT NULL DEFAULT 0 COMMENT 'it''s \\\\ here',\n" +
		"  `code` varchar(10) COLLATE utf8mb4_bin NULL,\n" +
		"  `total` int GENERATED ALWAYS AS (`n` * 2) STORED NULL,\n" +
		"  PRIMARY KEY (`id`),\n" +
		"  UNIQUE INDEX `code_u` (`code`),\n" +
		"  INDEX `n_idx` (`n`, `code`)\n" +
		") ENGINE=InnoDB COLLATE=utf8mb4_0900_ai_ci COMMENT='who did what'"
	if len(stmts) != 1 || stmts[0] != want {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(stmts, ";\n"), want)
	}
	if len(manual) != 1 || manual[0].Kind != db.AddIndex || manual[0].NewIndex.Name != "fx" {
		t.Errorf("manual = %+v", manual)
	}
}

func TestAlterTable(t *testing.T) {
	w := dialect{}.DDL()
	users := db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"}
	changes := []db.SchemaChange{
		{Kind: db.DropIndex, OldIndex: &db.Index{Name: "idx_old", Columns: []string{"x"}}},
		{Kind: db.DropIndex, OldIndex: &db.Index{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true}},
		{Kind: db.RenameColumn, Old: &db.Column{Name: "nm"}, New: &db.Column{Name: "nick"}},
		{Kind: db.RenameColumn, Old: &db.Column{Name: "a"}, New: &db.Column{Name: "b", Type: "bigint"}},
		{Kind: db.DropColumn, Old: &db.Column{Name: "legacy"}},
		{Kind: db.AddColumn, New: &db.Column{Name: "email", Type: "varchar(255)", Nullable: true}, After: "name"},
		{Kind: db.AddColumn, New: &db.Column{Name: "first", Type: "int", Nullable: true}},
		{
			Kind: db.AlterColumn, Old: &db.Column{Name: "name", Type: "varchar(50)"},
			New: &db.Column{Name: "name", Type: "varchar(100)", Comment: "who"}, Attrs: db.AttrType | db.AttrComment,
		},
		{Kind: db.AlterColumn, Old: &db.Column{Name: "a", Type: "int"}, New: &db.Column{Name: "b", Type: "bigint"}, Attrs: db.AttrType},
		{Kind: db.AlterColumn, Old: &db.Column{Name: "g", Type: "int"}, New: &db.Column{Name: "g", Type: "int", Generated: "1"}, Attrs: db.AttrGenerated},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "PRIMARY", Columns: []string{"id", "tenant"}, Unique: true, Primary: true}},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "email_u", Columns: []string{"email"}, Unique: true}},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "body_ft", Columns: []string{"body"}, Method: "fulltext"}},
		{Kind: db.AlterTableComment, NewValue: "people"},
		{Kind: db.AlterTableOption, Option: "engine", NewValue: "InnoDB"},
		{Kind: db.AlterTableOption, Option: "collation", NewValue: "utf8mb4_bin"},
		{Kind: db.ForeignKeyDiff, NewFK: &db.ForeignKey{Name: "fk"}},
	}
	stmts, manual := w.AlterTable(users, changes)
	want := "ALTER TABLE `app`.`users`\n" +
		"  DROP INDEX `idx_old`,\n" +
		"  DROP PRIMARY KEY,\n" +
		"  RENAME COLUMN `nm` TO `nick`,\n" +
		"  DROP COLUMN `legacy`,\n" +
		"  ADD COLUMN `email` varchar(255) NULL AFTER `name`,\n" +
		"  ADD COLUMN `first` int NULL FIRST,\n" +
		"  MODIFY COLUMN `name` varchar(100) NOT NULL COMMENT 'who',\n" +
		"  CHANGE COLUMN `a` `b` bigint NOT NULL,\n" +
		"  ADD PRIMARY KEY (`id`, `tenant`),\n" +
		"  ADD UNIQUE INDEX `email_u` (`email`),\n" +
		"  ADD FULLTEXT INDEX `body_ft` (`body`),\n" +
		"  COMMENT = 'people',\n" +
		"  ENGINE = InnoDB,\n" +
		"  COLLATE = utf8mb4_bin"
	if len(stmts) != 1 || stmts[0] != want {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(stmts, ";\n"), want)
	}
	if len(manual) != 2 || manual[0].Kind != db.AlterColumn || manual[1].Kind != db.ForeignKeyDiff {
		t.Errorf("manual = %+v", manual)
	}
	if stmts, manual := w.AlterTable(users, nil); stmts != nil || manual != nil {
		t.Errorf("no changes: %v %v", stmts, manual)
	}
}

func TestOnUpdate(t *testing.T) {
	for extra, want := range map[string]string{
		"":                            "",
		"default_generated":           "",
		"on update current_timestamp": "CURRENT_TIMESTAMP",
		"default_generated on update current_timestamp(3)": "CURRENT_TIMESTAMP(3)",
	} {
		if got := onUpdate(extra); got != want {
			t.Errorf("onUpdate(%q) = %q, want %q", extra, got, want)
		}
	}
	w := dialect{}.DDL()
	users := db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"}
	ct := "CURRENT_TIMESTAMP"
	col := db.Column{Name: "updated", Type: "timestamp", Default: &ct, OnUpdate: "CURRENT_TIMESTAMP", Comment: "x"}
	stmts, _ := w.AlterTable(users, []db.SchemaChange{{Kind: db.AlterColumn, Old: &col, New: &col, Attrs: db.AttrComment}})
	want := "ALTER TABLE `app`.`users`\n  MODIFY COLUMN `updated` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT 'x'"
	if len(stmts) != 1 || stmts[0] != want {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(stmts, ";\n"), want)
	}
}
