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

	"github.com/shepherd-yun/datavim/internal/db"
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

type (
	// fileSavedMsg reports a save: written (file), refused pending a
	// question (conflict), or failed (err).
	fileSavedMsg struct {
		tabID    int
		file     consoleFile
		conflict string
		quit     bool // :wq: close the tab once written
		err      error
	}
	// saveConfirmedMsg saves again, overwriting, after the user agreed.
	saveConfirmedMsg struct {
		tabID int
		path  string
		quit  bool
	}
)

func (m fileSavedMsg) TabID() int     { return m.tabID }
func (m saveConfirmedMsg) TabID() int { return m.tabID }

// writeSQLFile writes text to path atomically, through a symlink to its
// target. Unless force, it writes nothing and returns a question instead when
// path changed on disk since known was read, or when path is some other
// existing file (known == nil).
func writeSQLFile(path, text string, known *consoleFile, force bool) (consoleFile, string, error) {
	name := filepath.Base(path)
	fail := func(err error) (consoleFile, string, error) {
		return consoleFile{}, "", fmt.Errorf("write %s: %w", name, err)
	}
	target := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		target = r
	}
	perm := fs.FileMode(0o644)
	info, err := os.Stat(target)
	switch {
	case err == nil:
		if info.IsDir() {
			return fail(errors.New("is a directory"))
		}
		perm = info.Mode().Perm()
		if !force {
			asked := consoleFile{path: path}
			if known == nil {
				return asked, "overwrite " + tildePath(path) + "?", nil
			}
			if known.isNew || !info.ModTime().Equal(known.modTime) || info.Size() != known.size {
				return asked, name + " changed on disk since it was opened — overwrite?", nil
			}
		}
	case !errors.Is(err, fs.ErrNotExist):
		return fail(err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*.tmp")
	if err != nil {
		return fail(err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // fails harmlessly once renamed
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close()
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return fail(err)
	}
	if info, err = os.Stat(target); err != nil {
		return fail(err)
	}
	return consoleFile{path: path, modTime: info.ModTime(), size: info.Size(), saved: text}, "", nil
}

// lineCount counts the lines of text the way editors do.
func lineCount(text string) int {
	n := strings.Count(text, "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}
