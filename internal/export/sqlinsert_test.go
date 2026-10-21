package export

import (
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
)

func TestInsertMySQL(t *testing.T) {
	rs := &db.ResultSet{
		Columns: []db.ColumnInfo{
			{Name: "id", DatabaseType: "INT"},
			{Name: "name", DatabaseType: "VARCHAR"},
			{Name: "bin", DatabaseType: "VARBINARY"},
			{Name: "my`col", DatabaseType: "TEXT"},
		},
		Rows: [][]db.Value{
			{{Text: "1"}, {Text: "O'Brien"}, {Text: "0xDEAD"}, {Text: `C:\dir`}},
			{{Text: "2"}, {Null: true}, {Null: true}, {Text: ""}},
		},
	}
	want := "INSERT INTO `app`.`users` (`id`, `name`, `bin`, `my``col`) VALUES (1, 'O''Brien', 0xDEAD, 'C:\\\\dir');\n" +
		"INSERT INTO `app`.`users` (`id`, `name`, `bin`, `my``col`) VALUES (2, NULL, NULL, '');\n"
	got := encode(t, SQL, rs, Options{Driver: "mysql", Table: []string{"app", "users"}})
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestInsertPostgres(t *testing.T) {
	rs := &db.ResultSet{
		Columns: []db.ColumnInfo{{Name: "id", DatabaseType: "numeric"}, {Name: "b", DatabaseType: "bytea"}, {Name: "note", DatabaseType: "text"}},
		Rows: [][]db.Value{
			{{Text: "NaN"}, {Text: `\xdead`}, {Text: `C:\dir 'x'`}},
		},
	}
	want := `INSERT INTO "app"."Users" ("id", "b", "note") VALUES ('NaN', '\xdead', 'C:\dir ''x''');` + "\n"
	got := encode(t, SQL, rs, Options{Driver: "postgres", Table: []string{"app", "Users"}})
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestInsertDefaultTable(t *testing.T) {
	rs := &db.ResultSet{Columns: []db.ColumnInfo{{Name: "a"}}, Rows: [][]db.Value{{{Text: "x"}}}}
	if got := encode(t, SQL, rs, Options{Driver: "postgres"}); got != `INSERT INTO "query" ("a") VALUES ('x');`+"\n" {
		t.Fatalf("got %q", got)
	}
}
