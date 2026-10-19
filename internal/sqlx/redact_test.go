package sqlx

import "testing"

func TestRedactSQL(t *testing.T) {
	cases := []struct {
		d        Dialect
		in, want string
	}{
		{MySQL, "CREATE USER 'a'@'%' IDENTIFIED BY 's3cr3t'", "CREATE USER 'a'@'%' IDENTIFIED BY '***'"},
		{MySQL, "alter user a identified with mysql_native_password by 'x'", "alter user a identified with mysql_native_password by '***'"},
		{Postgres, "ALTER ROLE app WITH PASSWORD 'it''s'", "ALTER ROLE app WITH PASSWORD '***'"},
		{SQLServer, "CREATE LOGIN app WITH PASSWORD = N'P@ss', CHECK_POLICY = OFF", "CREATE LOGIN app WITH PASSWORD = '***', CHECK_POLICY = OFF"},
		{Oracle, "ALTER USER scott IDENTIFIED BY tiger", "ALTER USER scott IDENTIFIED BY '***'"},
		{Oracle, `ALTER USER scott IDENTIFIED BY "Ti ger"`, "ALTER USER scott IDENTIFIED BY '***'"},
		{MySQL, "SET PASSWORD FOR 'a'@'%' = 'x'", "SET PASSWORD FOR 'a'@'%' = '***'"},
		{MySQL, "SET PASSWORD = 'x'", "SET PASSWORD = '***'"},
		{Postgres, "ALTER ROLE app PASSWORD NULL", "ALTER ROLE app PASSWORD NULL"},
		{MySQL, "/* hi */ CREATE USER u IDENTIFIED BY 'p'", "/* hi */ CREATE USER u IDENTIFIED BY '***'"},
		{MySQL, "SELECT password FROM users WHERE name = 'IDENTIFIED BY x'", "SELECT password FROM users WHERE name = 'IDENTIFIED BY x'"},
		{MySQL, "UPDATE t SET note = 'PASSWORD x'", "UPDATE t SET note = 'PASSWORD x'"},
		{MySQL, "", ""},
		{MySQL, "ALTER USER a IDENTIFIED BY", "ALTER USER a IDENTIFIED BY"},
		{MySQL, "ALTER USER a PASSWORD", "ALTER USER a PASSWORD"},
		{MySQL, "CREATE USER u IDENTIFIED BY 'unterminated", "CREATE USER u IDENTIFIED BY '***'"},
		// not the batch's first statement
		{
			SQLServer, "IF NOT EXISTS (SELECT 1 FROM sys.server_principals WHERE name = 'app') CREATE LOGIN app WITH PASSWORD = N'p'",
			"IF NOT EXISTS (SELECT 1 FROM sys.server_principals WHERE name = 'app') CREATE LOGIN app WITH PASSWORD = '***'",
		},
		{SQLServer, "USE master; CREATE LOGIN app WITH PASSWORD = 'p'", "USE master; CREATE LOGIN app WITH PASSWORD = '***'"},
		{MySQL, "SELECT 1; CREATE USER u IDENTIFIED BY 'p'", "SELECT 1; CREATE USER u IDENTIFIED BY '***'"},
		{Postgres, "DO $$ BEGIN CREATE ROLE app LOGIN PASSWORD 'p'; END $$", "DO $$ BEGIN CREATE ROLE app LOGIN PASSWORD '***'; END $$"},
		{Postgres, "DO $body$ BEGIN CREATE ROLE app PASSWORD 'p'; END $body$", "DO $body$ BEGIN CREATE ROLE app PASSWORD '***'; END $body$"},
		{Postgres, "ALTER ROLE app PASSWORD $$p$$", "ALTER ROLE app PASSWORD '***'"},
		// old passwords
		{Oracle, "ALTER USER scott IDENTIFIED BY lion REPLACE tiger", "ALTER USER scott IDENTIFIED BY '***' REPLACE '***'"},
		{MySQL, "ALTER USER u IDENTIFIED BY 'new' REPLACE 'old'", "ALTER USER u IDENTIFIED BY '***' REPLACE '***'"},
		{MySQL, "SET PASSWORD = 'new' REPLACE 'old'", "SET PASSWORD = '***' REPLACE '***'"},
		{SQLServer, "ALTER LOGIN app WITH PASSWORD = 'new' OLD_PASSWORD = 'old'", "ALTER LOGIN app WITH PASSWORD = '***' OLD_PASSWORD = '***'"},
		{MySQL, "SELECT REPLACE(name, 'a', 'b') FROM t", "SELECT REPLACE(name, 'a', 'b') FROM t"},
		// other forms
		{MySQL, "GRANT ALL ON *.* TO 'u'@'%' IDENTIFIED BY 'pw'", "GRANT ALL ON *.* TO 'u'@'%' IDENTIFIED BY '***'"},
		{SQLServer, "EXEC sp_addlogin 'app', 'pw'", "EXEC sp_addlogin '***', '***'"},
		{SQLServer, "EXEC sp_password 'old', 'new', 'app'; SELECT 'x'", "EXEC sp_password '***', '***', '***'; SELECT 'x'"},
	}
	for _, c := range cases {
		if got := RedactSQL(c.in, c.d); got != c.want {
			t.Errorf("RedactSQL(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}
