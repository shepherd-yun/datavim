package sqlite

import (
	"regexp"
	"strconv"
	"strings"
)

// noSQLite3Marker is what the commands below print when the server has no
// sqlite3, whatever language its shell answers in.
const noSQLite3Marker = "DATAVIM_NO_SQLITE3"

// shQuote quotes s for a POSIX shell.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// script wraps a command for the user's login shell, which need not be POSIX:
// sh runs it, after making sure sqlite3 exists.
func script(command string) string {
	return "sh -c " + shQuote(`command -v sqlite3 >/dev/null 2>&1 || { echo `+noSQLite3Marker+`; exit 127; }; `+command)
}

// startCommand starts sqlite3 on path, printing its PID first (exec keeps it).
// mode=rw and mode=ro never create the file. The argument is a URI, with ?, #
// and % escaped; a leading ~/ is left to the server's $HOME. -init /dev/null
// keeps the user's ~/.sqliterc out of the protocol.
func startCommand(path string, readOnly, safe bool) string {
	mode := "rw"
	if readOnly {
		mode = "ro"
	}
	args := "-batch -init /dev/null"
	if readOnly {
		args += " -readonly"
	}
	if safe {
		args += " -safe"
	}
	file := ""
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		file = `"file:$HOME"/` + shQuote(uriPath(rest)+"?mode="+mode)
	} else {
		file = shQuote("file:" + uriPath(path) + "?mode=" + mode)
	}
	return script("echo PID=$$; exec sqlite3 " + args + " " + file + " 2>&1")
}

// versionCommand prints the version of the server's sqlite3.
func versionCommand() string { return script("exec sqlite3 -batch -version 2>&1") }

var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.\d+(\s|$)`)

// parseVersion reads the output of versionCommand.
func parseVersion(out string) (major, minor int, ok bool) {
	m := versionRe.FindStringSubmatch(out)
	if m == nil {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	return major, minor, err1 == nil && err2 == nil
}
