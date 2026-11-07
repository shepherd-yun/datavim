package sqlite

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// fakeBin writes a sqlite3 that prints its arguments one per line.
func fakeBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"
	if err := os.WriteFile(filepath.Join(dir, "sqlite3"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runCommand(t *testing.T, command, path, dir, home string) (string, int) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + path, "HOME=" + home}
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func TestStartCommand(t *testing.T) {
	bin := fakeBin(t)
	home := t.TempDir()
	path := bin + ":/usr/bin:/bin"
	tests := []struct {
		name, file string
		ro, safe   bool
		want       []string
	}{
		{"spaces", "/a b/c.db", false, false, []string{"-batch", "-init", "/dev/null", "file:/a b/c.db?mode=rw"}},
		{"quote", "/a'b.db", false, false, []string{"-batch", "-init", "/dev/null", "file:/a'b.db?mode=rw"}},
		{"unicode", "/数据/c.db", false, false, []string{"-batch", "-init", "/dev/null", "file:/数据/c.db?mode=rw"}},
		{"uri chars", "/a?b#c%d.db", false, false, []string{"-batch", "-init", "/dev/null", "file:/a%3Fb%23c%25d.db?mode=rw"}},
		{"home", "~/x y.db", false, false, []string{"-batch", "-init", "/dev/null", "file:" + home + "/x y.db?mode=rw"}},
		{"injection", "$(touch pwned).db", false, false, []string{"-batch", "-init", "/dev/null", "file:$(touch pwned).db?mode=rw"}},
		{"backticks", "/`touch pwned`.db", false, false, []string{"-batch", "-init", "/dev/null", "file:/`touch pwned`.db?mode=rw"}},
		{"read only", "/c.db", true, false, []string{"-batch", "-init", "/dev/null", "-readonly", "file:/c.db?mode=ro"}},
		{"safe", "/c.db", true, true, []string{"-batch", "-init", "/dev/null", "-readonly", "-safe", "file:/c.db?mode=ro"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd := t.TempDir()
			out, code := runCommand(t, startCommand(tt.file, tt.ro, tt.safe), path, cwd, home)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if !strings.HasPrefix(lines[0], "PID=") {
				t.Fatalf("first line %q", lines[0])
			}
			if diff := cmp.Diff(tt.want, lines[1:]); diff != "" {
				t.Fatalf("argv (-want +got):\n%s", diff)
			}
			if _, err := os.Stat(filepath.Join(cwd, "pwned")); err == nil {
				t.Fatal("the path ran as a command")
			}
		})
	}
}

func TestStartCommandUserShellQuoting(t *testing.T) {
	// the command is one word for sh -c, whatever the path holds
	if got := startCommand("/x'y", false, false); !strings.HasPrefix(got, "sh -c '") {
		t.Fatalf("got %q", got)
	}
}

func TestMissingBinaryCommands(t *testing.T) {
	empty := t.TempDir()
	for name, c := range map[string]string{
		"start":   startCommand("/c.db", false, false),
		"version": versionCommand(),
	} {
		// only sh is reachable: a PATH without sqlite3
		bin := t.TempDir()
		for _, tool := range []string{"sh"} {
			src, err := exec.LookPath(tool)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(src, filepath.Join(bin, tool)); err != nil {
				t.Fatal(err)
			}
		}
		out, code := runCommand(t, c, bin, empty, empty)
		if code != 127 || !strings.Contains(out, noSQLite3Marker) {
			t.Errorf("%s: exit %d, output %q", name, code, out)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	bin := fakeBin(t)
	out, code := runCommand(t, versionCommand(), bin+":/usr/bin:/bin", t.TempDir(), t.TempDir())
	if code != 0 || !strings.Contains(out, "-version") {
		t.Fatalf("exit %d output %q", code, out)
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in           string
		major, minor int
		ok           bool
	}{
		{"3.54.0 2026-04-09 12:25:13 8fa8", 3, 54, true},
		{"3.26.0 2018-12-01", 3, 26, true},
		{"3.7.17 2013-05-20\n", 3, 7, true},
		{"sqlite3: command not found", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tt := range tests {
		major, minor, ok := parseVersion(tt.in)
		if major != tt.major || minor != tt.minor || ok != tt.ok {
			t.Errorf("parseVersion(%q) = %d, %d, %v", tt.in, major, minor, ok)
		}
	}
}
