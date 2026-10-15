package ui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// maxFileSize bounds the files :e opens into a console.
const maxFileSize = 2 << 20

// consoleFile is the .sql file a console edits.
type consoleFile struct {
	path    string // absolute and clean
	modTime time.Time
	size    int64
	saved   string // the text on disk when read or last written
	isNew   bool   // there is no such file on disk yet
}

func (f *consoleFile) name() string { return filepath.Base(f.path) }

func fileTabKey(path, conn string, ns db.Namespace) string {
	return "file:" + path + "|" + consoleTabKey(conn, ns)
}

// absPath expands a leading ~ and makes p absolute and clean.
func absPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	return filepath.Abs(p)
}

// tildePath shortens a path under the home directory to ~/….
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	rel, err := filepath.Rel(home, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return filepath.Join("~", rel)
}

// shortPath is tildePath cut from the left to at most width runes, so the
// file name stays in view.
func shortPath(p string, width int) string {
	r := []rune(tildePath(p))
	if len(r) <= width {
		return string(r)
	}
	return "…" + string(r[len(r)-width+1:])
}

func humanSize(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }

// fileReadMsg carries a file :e read for a new console on conn.
type fileReadMsg struct {
	conn string
	ns   db.Namespace
	file consoleFile
	err  error
}

func readSQLFile(conn string, ns db.Namespace, path string) tea.Cmd {
	return func() tea.Msg {
		f, err := loadSQLFile(path)
		return fileReadMsg{conn: conn, ns: ns, file: f, err: err}
	}
}

// loadSQLFile reads path; a missing file is a new, empty one.
func loadSQLFile(path string) (consoleFile, error) {
	name := filepath.Base(path)
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return consoleFile{path: path, isNew: true}, nil
	case err != nil:
		return consoleFile{}, err
	case info.IsDir():
		return consoleFile{}, fmt.Errorf("%s is a directory", name)
	case info.Size() > maxFileSize:
		return consoleFile{}, fmt.Errorf("%s is %s: too large for the editor", name, humanSize(info.Size()))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return consoleFile{}, err
	}
	text := string(b)
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return consoleFile{}, fmt.Errorf("%s is not a UTF-8 text file", name)
	}
	text = strings.TrimPrefix(text, "\ufeff")
	return consoleFile{path: path, modTime: info.ModTime(), size: info.Size(), saved: text}, nil
}
