package sqlite

import (
	"strings"
	"testing"
)

func TestCheckStmt(t *testing.T) {
	dot := "dot commands are not supported"
	bad := []struct {
		stmt string
		ro   bool
		want string
	}{
		{".shell ls", false, dot},
		{"  .read x", false, dot},
		{"/* c */ .shell ls", false, dot},
		{"-- c\n.shell ls", false, dot},
		{"select 1; .shell ls", false, dot},
		{"\n\n.system ls", true, dot},
		{"#x\n.shell ls", false, "#"},
		// a line holding only / or go ends the statement in the shell, and
		// the next line would be a dot command
		{"select 1\ngo\n.shell ls", false, "go"},
		{"select 1\n/\n.shell ls", false, "/"},
		{"select 1\n  GO  -- c\n.shell ls", false, "go"},
		{"select 'a", false, "incomplete"},
		{"/* x", false, "incomplete"},
		{"create trigger t after insert on q begin select 1;", false, "incomplete"},
		{"select 1\x00", false, "NUL"},
		{"select edit('x')", false, "edit"},
		{`select "load_extension"('x')`, false, "load_extension"},
		{"select EDIT /* c */ ('x')", false, "edit"},
		{"select 1; select edit('x')", false, "edit"},
		{"select edit('x')", true, "edit"},
		{"select writefile('a', 'b')", true, "writefile"},
		{"select readfile('a')", true, "readfile"},
		{"select * from fsdir('.')", true, "fsdir"},
		{"ATTACH 'x' AS y", true, "ATTACH"},
		{"/* c */ attach 'x' as y", true, "ATTACH"},
		{"VACUUM INTO '/tmp/copy.db'", true, "VACUUM"},
		{"CREATE VIRTUAL TABLE temp.z USING zipfile('/tmp/x.zip')", true, "virtual"},
	}
	for _, tt := range bad {
		err := checkStmt(tt.stmt, tt.ro)
		if err == nil {
			t.Errorf("checkStmt(%q, %v) passed", tt.stmt, tt.ro)
		} else if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("checkStmt(%q, %v) = %v, want %q in it", tt.stmt, tt.ro, err, tt.want)
		}
	}
	good := []struct {
		stmt string
		ro   bool
	}{
		{"select '\n.shell x'", false},
		{"select '\ngo\n'", false},
		{"select /* \ngo\n */ 1", false},
		{"select 1 as go", false},
		{"select go from t", false},
		{"select 4\n/ 2", false},
		{"select a.b from a", false},
		{"-- only comment", false},
		{"", false},
		{"   ", true},
		{"select writefile('a', 'b')", false},
		{"ATTACH 'x' AS y", false},
		{"VACUUM", false},
		{"select edit from t", true},
		{"select 'edit(1)'", true},
		{"create trigger t after insert on q begin select 1; end", false},
		{"select 1;\nselect 2;", true},
	}
	for _, tt := range good {
		if err := checkStmt(tt.stmt, tt.ro); err != nil {
			t.Errorf("checkStmt(%q, %v): %v", tt.stmt, tt.ro, err)
		}
	}
}

func TestProtectCRLF(t *testing.T) {
	tests := []struct{ in, want string }{
		{"select 1", "select 1"},
		{"select 1\r\nfrom t", "select 1\r\nfrom t"},
		{"select 'a\r\nb'", "select ('a'||char(13)||char(10)||'b')"},
		{"select 'it''s\r\n', 'c' -- '\r\n", "select ('it''s'||char(13)||char(10)||''), 'c' -- '\r\n"},
		{"select 'a\nb\rc'", "select 'a\nb\rc'"},
	}
	for _, tt := range tests {
		if got := protectCRLF(tt.in); got != tt.want {
			t.Errorf("protectCRLF(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
