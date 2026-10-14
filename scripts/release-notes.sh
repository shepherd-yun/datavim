#!/bin/sh
# Prints the CHANGELOG.md section of a version (without its heading):
#   scripts/release-notes.sh 1.0.0
set -eu
v=${1:?usage: release-notes.sh VERSION}
notes=$(awk -v v="$v" '
	index($0, "## [" v "]") == 1 { p = 1; next }
	/^## \[/ { p = 0 }
	/^\[[^]]+\]: / { p = 0 }
	p
' CHANGELOG.md)
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
	echo "release-notes: no CHANGELOG.md section for $v" >&2
	exit 1
fi
printf '%s\n' "$notes"
