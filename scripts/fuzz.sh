#!/usr/bin/env bash
# Runs every fuzz target, in the library and in the SFTP module, for a while each.
# Go runs one fuzz target at a time, so they run one after another.
# Usage: scripts/fuzz.sh [time-per-target, default 30s]
set -euo pipefail
cd "$(dirname "$0")/.."
fuzztime="${1:-30s}"
log="$(mktemp)"
trap 'rm -f "$log"' EXIT
# Each line: the module's directory, then its package with fuzz targets.
for module in "pkg/scp ." "pkg/sftp ."; do
	read -r dir pkg <<<"$module"
	targets="$(cd "$dir" && go test -list '^Fuzz' "$pkg" | grep '^Fuzz')"
	for target in $targets; do
		echo "== $dir $target ($fuzztime)"
		if (cd "$dir" && go test -run '^$' -fuzz "^${target}\$" -fuzztime "$fuzztime" "$pkg") >"$log" 2>&1; then
			tail -1 "$log"
			continue
		fi
		cat "$log"
		# Go's fuzzing engine sometimes stops at the time limit with nothing but
		# "context deadline exceeded". A real finding always writes its input
		# under testdata/fuzz; without one there is nothing to report.
		if grep -q 'context deadline exceeded' "$log" && ! grep -q 'Failing input written' "$log"; then
			echo "$target: stopped at the time limit with no failing input; not a finding"
			continue
		fi
		exit 1
	done
done
