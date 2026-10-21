package esx

import (
	"fmt"
	"regexp"
	"time"
)

// RestoreTargets is what each index of a snapshot restores as under a
// rename_pattern / rename_replacement; ES uses Java's $1, which Go's
// regexp shares. An empty pattern keeps the names.
func RestoreTargets(indices []string, pattern, replacement string) ([]string, error) {
	if pattern == "" {
		return append([]string(nil), indices...), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("rename pattern: %w", err)
	}
	out := make([]string, len(indices))
	from := map[string]string{}
	for i, name := range indices {
		out[i] = re.ReplaceAllString(name, replacement)
		if prev, ok := from[out[i]]; ok {
			return nil, fmt.Errorf("%s and %s both restore as %s", prev, name, out[i])
		}
		from[out[i]] = name
	}
	return out, nil
}

// SnapshotName is the default name of a new snapshot.
func SnapshotName(now time.Time) string {
	return "snapshot-" + now.UTC().Format("2006.01.02-150405")
}
