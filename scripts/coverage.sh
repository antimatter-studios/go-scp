#!/usr/bin/env bash
# Runs one module's tests with coverage and prints the total statement coverage of
# its packages, e.g. 81.3: the library in pkg/ by default, or the module in the
# directory given second, such as pkg/sftp. The example programs and test helpers
# are left out: scripts/examples.sh runs the examples, and the helpers only serve
# the tests.
# Usage: scripts/coverage.sh [profile-file] [module-dir]
set -euo pipefail
cd "$(dirname "$0")/.."
profile="$(cd "$(dirname "${1:-coverage.out}")" && pwd)/$(basename "${1:-coverage.out}")"
module="${2:-.}"
packages=./pkg/...
if [ "$module" != "." ]; then
	packages=./...
fi
cd "$module"
go test -race -count=1 -coverprofile="$profile" "$packages" >&2
go tool cover -func="$profile" | awk '/^total:/ { sub("%", "", $3); print $3 }'
