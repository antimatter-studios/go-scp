#!/usr/bin/env bash
# Fails unless every statement in pkg/ is covered by the tests, in the library and
# in the SFTP module alike, naming the functions that are not.
# Usage: scripts/coverage-gate.sh
set -euo pipefail
cd "$(dirname "$0")/.."
profile="$(mktemp)"
trap 'rm -f "$profile"' EXIT
failed=0
for module in pkg/scp pkg/sftp; do
	total="$(scripts/coverage.sh "$profile" "$module")"
	if [ "$total" != "100.0" ]; then
		echo "$module: coverage is ${total}%, and must be 100%. Not fully covered:" >&2
		(cd "$module" && go tool cover -func="$profile") | awk '$1 != "total:" && $3 != "100.0%"' >&2
		failed=1
	else
		echo "$module: coverage is 100%"
	fi
done
exit "$failed"
