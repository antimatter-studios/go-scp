#!/usr/bin/env bash
# Starts the two-host test network, runs the integration tests of the library and
# of the SFTP module against it, and takes it down again, whatever the outcome.
# Usage: scripts/integration.sh [go test flags...]
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=scripts/lib/network.sh
source scripts/lib/network.sh

network_up

export GOSCP_HOST_A="127.0.0.1:${HOST_A_PORT}"
export GOSCP_HOST_B="127.0.0.1:${HOST_B_PORT}"
export GOSCP_KEY="$KEYS_DIR/id_ed25519"
go test -tags integration -race -count=1 "$@" ./pkg/scp/
(cd pkg/sftp && go test -tags integration -race -count=1 "$@" ./...)
