#!/usr/bin/env bash
# Runs every fuzz target, in the library and in the SFTP module, for a while each.
# Go runs one fuzz target at a time, so they run one after another.
# Usage: scripts/fuzz.sh [time-per-target, default 30s]
set -euo pipefail
cd "$(dirname "$0")/.."
fuzztime="${1:-30s}"
# Each line: the module's directory, then its package with fuzz targets.
for module in ". ./pkg/scp" "pkg/sftp ."; do
	read -r dir pkg <<<"$module"
	targets="$(cd "$dir" && go test -list '^Fuzz' "$pkg" | grep '^Fuzz')"
	for target in $targets; do
		echo "== $dir $target ($fuzztime)"
		(cd "$dir" && go test -run '^$' -fuzz "^${target}\$" -fuzztime "$fuzztime" "$pkg")
	done
done
