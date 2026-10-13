#!/usr/bin/env bash
# Fails unless every statement in pkg/ is covered by the tests, naming the functions
# that are not.
# Usage: scripts/coverage-gate.sh
set -euo pipefail
cd "$(dirname "$0")/.."
profile="$(mktemp)"
trap 'rm -f "$profile"' EXIT
total="$(scripts/coverage.sh "$profile")"
if [ "$total" != "100.0" ]; then
	echo "coverage is ${total}%, and must be 100%. Not fully covered:" >&2
	go tool cover -func="$profile" | awk '$1 != "total:" && $3 != "100.0%"' >&2
	exit 1
fi
echo "coverage is 100%"
