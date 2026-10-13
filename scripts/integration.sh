#!/usr/bin/env bash
# Starts the two-host test network, runs the integration tests against it, and
# takes it down again, whatever the outcome.
# Usage: scripts/integration.sh [go test flags...]
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=scripts/lib/network.sh
source scripts/lib/network.sh

network_up

GOSCP_HOST_A="127.0.0.1:${HOST_A_PORT}" \
GOSCP_HOST_B="127.0.0.1:${HOST_B_PORT}" \
GOSCP_KEY="$KEYS_DIR/id_ed25519" \
	go test -tags integration -race -count=1 "$@" ./pkg/scp/
