#!/usr/bin/env bash
# Runs the tests with coverage and prints the total statement coverage, e.g. 81.3.
# Usage: scripts/coverage.sh [profile-file]
set -euo pipefail
profile="${1:-coverage.out}"
go test -race -count=1 -coverprofile="$profile" ./pkg/scp/... >&2
go tool cover -func="$profile" | awk '/^total:/ { sub("%", "", $3); print $3 }'
