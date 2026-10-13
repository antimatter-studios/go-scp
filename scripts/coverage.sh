#!/usr/bin/env bash
# Runs the library's tests with coverage and prints the total statement coverage of
# pkg/, e.g. 81.3. The example programs and test helpers are left out: scripts/
# examples.sh runs the examples, and the helpers only serve the tests.
# Usage: scripts/coverage.sh [profile-file]
set -euo pipefail
profile="${1:-coverage.out}"
go test -race -count=1 -coverprofile="$profile" ./pkg/scp/... >&2
go tool cover -func="$profile" | awk '/^total:/ { sub("%", "", $3); print $3 }'
