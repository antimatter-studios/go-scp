#!/usr/bin/env bash
# Starts the two-host test network, runs the integration tests against it, and
# takes it down again, whatever the outcome.
# Usage: scripts/integration.sh [go test flags...]
set -euo pipefail
cd "$(dirname "$0")/.."

compose=(docker compose -f test/network/compose.yaml)
keys="$(mktemp -d)"
cleanup() {
	"${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
	rm -rf "$keys"
}
trap cleanup EXIT

export KEYS_DIR="$keys" HOST_A_PORT="${HOST_A_PORT:-2221}" HOST_B_PORT="${HOST_B_PORT:-2222}"
go run ./test/network/keygen "$keys"
"${compose[@]}" up --build --detach --wait --quiet-pull

GOSCP_HOST_A="127.0.0.1:${HOST_A_PORT}" \
GOSCP_HOST_B="127.0.0.1:${HOST_B_PORT}" \
GOSCP_KEY="$keys/id_ed25519" \
	go test -tags integration -race -count=1 "$@" ./pkg/scp/
