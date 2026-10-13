#!/usr/bin/env bash
# Runs every fuzz target in pkg/scp for a while each. Go runs one fuzz target at a
# time, so they run one after another.
# Usage: scripts/fuzz.sh [time-per-target, default 30s]
set -euo pipefail
cd "$(dirname "$0")/.."
fuzztime="${1:-30s}"
targets="$(go test -list '^Fuzz' ./pkg/scp | grep '^Fuzz')"
for target in $targets; do
	echo "== $target ($fuzztime)"
	go test -run '^$' -fuzz "^${target}\$" -fuzztime "$fuzztime" ./pkg/scp
done
