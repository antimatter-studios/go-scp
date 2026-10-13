# shellcheck shell=bash
# Starts and stops the two-host test network (test/network). Source it, then call
# network_up; it takes the network down again when the calling script exits.
#
# After network_up: KEYS_DIR holds the test key pair, HOST_A_PORT and HOST_B_PORT
# are the loopback ports of host-a and host-b, and the tester user on each accepts
# the key.

network_compose=(docker compose -f test/network/compose.yaml)

network_down() {
	"${network_compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
	rm -rf "${KEYS_DIR:-}"
}

network_up() {
	KEYS_DIR="$(mktemp -d)"
	export KEYS_DIR HOST_A_PORT="${HOST_A_PORT:-2221}" HOST_B_PORT="${HOST_B_PORT:-2222}"
	trap network_down EXIT
	go run ./test/network/keygen "$KEYS_DIR"
	"${network_compose[@]}" up --build --detach --wait --quiet-pull
}
