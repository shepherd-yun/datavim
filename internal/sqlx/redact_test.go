package sqlx

import "testing"

func TestRedactSQL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"CREATE USER 'a'@'%' IDENTIFIED BY 's3cr3t'", "CREATE USER 'a'@'%' IDENTIFIED BY '***'"},
		{"alter user a identified with mysql_native_password by 'x'", "alter user a identified with mysql_native_password by '***'"},
		{"ALTER ROLE app WITH PASSWORD 'it''s'", "ALTER ROLE app WITH PASSWORD '***'"},
		{"CREATE LOGIN app WITH PASSWORD = N'P@ss', CHECK_POLICY = OFF", "CREATE LOGIN app WITH PASSWORD = '***', CHECK_POLICY = OFF"},
		{"ALTER USER scott IDENTIFIED BY tiger", "ALTER USER scott IDENTIFIED BY '***'"},
		{`ALTER USER scott IDENTIFIED BY "Ti ger"`, "ALTER USER scott IDENTIFIED BY '***'"},
		{"SET PASSWORD FOR 'a'@'%' = 'x'", "SET PASSWORD FOR 'a'@'%' = '***'"},
		{"SET PASSWORD = 'x'", "SET PASSWORD = '***'"},
		{"ALTER ROLE app PASSWORD NULL", "ALTER ROLE app PASSWORD NULL"},
		{"/* hi */ CREATE USER u IDENTIFIED BY 'p'", "/* hi */ CREATE USER u IDENTIFIED BY '***'"},
		{"SELECT password FROM users WHERE name = 'IDENTIFIED BY x'", "SELECT password FROM users WHERE name = 'IDENTIFIED BY x'"},
		{"UPDATE t SET note = 'PASSWORD x'", "UPDATE t SET note = 'PASSWORD x'"},
		{"", ""},
		{"ALTER USER a IDENTIFIED BY", "ALTER USER a IDENTIFIED BY"},
		{"ALTER USER a PASSWORD", "ALTER USER a PASSWORD"},
		{"CREATE USER u IDENTIFIED BY 'unterminated", "CREATE USER u IDENTIFIED BY '***'"},
	}
	for _, c := range cases {
		if got := RedactSQL(c.in); got != c.want {
			t.Errorf("RedactSQL(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}
