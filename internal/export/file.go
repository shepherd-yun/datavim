package export

import (
	"errors"
	"os"
	"strings"
	"time"
)

// FileName builds "<base>-<YYYYMMDD-HHMMSS>.<ext>"; characters other than
// [A-Za-z0-9_.-] in base become "_" and an empty base becomes "query".
func FileName(base string, f Format, now time.Time) string {
	if base == "" {
		base = "query"
	}
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			return r
		}
		return '_'
	}, base)
	return safe + "-" + now.Format("20060102-150405") + "." + string(f)
}

// ToFile creates path (failing if it exists), writes through fill and
// closes the writer. On any error the partial file is removed.
func ToFile(path string, f Format, o Options, fill func(Writer) error) (err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	w, err := New(f, file, o)
	if err != nil {
		return err
	}
	if err := fill(w); err != nil {
		return err
	}
	return w.Close()
}
