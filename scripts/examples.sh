#!/usr/bin/env bash
# Runs every example in examples/ against the two-host test network and checks what
# each one produced, so an example that stops working fails the build.
# Usage: scripts/examples.sh
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=scripts/lib/network.sh
source scripts/lib/network.sh

network_up

work="$(mktemp -d)"
bin="$work/bin"
trap 'network_down; rm -rf "$work"' EXIT
mkdir -p "$bin"
go build -o "$bin/" ./examples/...
# The SFTP example is a module of its own, as pkg/sftp is.
(cd examples/sftp && go build -o "$bin/sftp" .)

key="$KEYS_DIR/id_ed25519"
a=(-addr "127.0.0.1:$HOST_A_PORT" -user tester -key "$key" -insecure)
b=(-addr "127.0.0.1:$HOST_B_PORT" -user tester -key "$key" -insecure)
home=/home/tester

step() { printf '\n== %s\n' "$*"; }
same() {
	if ! cmp -s "$1" "$2"; then
		echo "FAIL: $1 and $2 differ" >&2
		exit 1
	fi
}

# Local files the examples send.
mkdir -p "$work/project/src" "$work/logs"
printf 'notes\n' >"$work/notes.txt"
printf 'read me\n' >"$work/project/README"
printf 'package main\n' >"$work/project/src/main.go"
printf 'A\n' >"$work/logs/a.txt"
printf 'B\n' >"$work/logs/b.txt"
printf 'C\n' >"$work/logs/c.log"
head -c 40000 /dev/zero >"$work/big.bin"

step upload and download
"$bin/upload" "${a[@]}" -from "$work/notes.txt" -to "$home/notes.txt"
"$bin/download" "${a[@]}" -from "$home/notes.txt" -to "$work/notes.back"
same "$work/notes.txt" "$work/notes.back"

step copy-tree
mkdir -p "$work/restored"
"$bin/copy-tree" "${a[@]}" -dir "$work/project" -remote "$home" -back "$work/restored"
diff -r "$work/project" "$work/restored/project"

step wildcards
"$bin/copy-tree" "${a[@]}" -dir "$work/logs" -remote "$home" -back "$work/restored"
mkdir -p "$work/matches"
"$bin/wildcards" "${a[@]}" -pattern "$home/logs/*.txt" -to "$work/matches"
test "$(find "$work/matches" -type f -exec basename {} \; | sort | tr '\n' ' ')" = "a.txt b.txt "

step preserve
TZ=UTC touch -t 202001020304.05 "$work/notes.txt"
"$bin/preserve" "${a[@]}" -from "$work/notes.txt" -to "$home/kept.txt" | tee "$work/preserve.out"
grep -q 'modified 2020-01-02T03:04:05Z' "$work/preserve.out"

step sudo
"$bin/sudo" "${a[@]}" -to /srv/root-only/settings.txt -text hello
"$bin/download" "${a[@]}" -from /srv/root-only/settings.txt -to "$work/settings.txt"
test "$(cat "$work/settings.txt")" = hello

step bandwidth
"$bin/bandwidth" "${a[@]}" -from "$work/big.bin" -to "$home/big.bin" -kbit 800
"$bin/download" "${a[@]}" -from "$home/big.bin" -to "$work/big.back"
same "$work/big.bin" "$work/big.back"

step between-hosts
"$bin/between-hosts" "${a[@]}" -dst-addr "127.0.0.1:$HOST_B_PORT" -dst-user tester -dst-key "$key" -dst-insecure \
	-from "$home/project" -to "$home"
"$bin/download" "${b[@]}" -from "$home/project/README" -to "$work/between.README"
same "$work/project/README" "$work/between.README"

step on-host
"$bin/on-host" "${a[@]}" -from "$home/project" -to "host-b:/tmp"
"$bin/download" "${b[@]}" -from /tmp/project/src/main.go -to "$work/onhost.main.go"
same "$work/project/src/main.go" "$work/onhost.main.go"

step on-host -forward-agent
keyless=(-addr "127.0.0.1:$HOST_A_PORT" -user keyless -key "$key" -insecure)
"$bin/upload" "${keyless[@]}" -from "$work/notes.txt" -to /home/keyless/notes.txt
# keyless holds no key on host-a, so the copy reaches host-b with the key in an
# agent that lives only as long as the example.
ssh-agent sh -c 'ssh-add -q "$1" && shift && exec "$@"' _ "$key" \
	"$bin/on-host" "${keyless[@]}" -forward-agent -from /home/keyless/notes.txt -to host-b:/tmp/agent.notes.txt
"$bin/download" "${b[@]}" -from /tmp/agent.notes.txt -to "$work/agent.notes.txt"
same "$work/notes.txt" "$work/agent.notes.txt"

step sftp
mkdir -p "$work/sftp.back" "$work/sftp.auto" "$work/sftp.program"
"$bin/sftp" "${a[@]}" -dir "$work/project" -remote "$home" -back "$work/sftp.back"
diff -r "$work/project" "$work/sftp.back/project"
"$bin/sftp" "${a[@]}" -auto -dir "$work/project" -remote "$home" -back "$work/sftp.auto"
diff -r "$work/project" "$work/sftp.auto/project"
for program in /usr/lib/openssh/sftp-server /usr/libexec/sftp-server /usr/lib/sftp-server; do
	if [ -x "$program" ]; then
		mkdir -p "$work/sftp.local"
		"$bin/sftp" -program "$program" -dir "$work/project" -remote "$work/sftp.local" -back "$work/sftp.program"
		diff -r "$work/project" "$work/sftp.program/project"
		break
	fi
done

step server
mkdir -p "$work/serverkey" "$work/share"
go run ./test/network/keygen "$work/serverkey"
"$bin/server" -listen 127.0.0.1:2299 -root "$work/share" \
	-host-key "$work/serverkey/id_ed25519" -authorized-key "$key.pub" &
server=$!
trap 'kill "$server" 2>/dev/null; network_down; rm -rf "$work"' EXIT
for _ in $(seq 50); do
	if (echo >/dev/tcp/127.0.0.1/2299) 2>/dev/null; then break; fi
	sleep 0.1
done
local=(-addr 127.0.0.1:2299 -user tester -key "$key" -insecure)
"$bin/upload" "${local[@]}" -from "$work/notes.txt" -to notes.txt
same "$work/notes.txt" "$work/share/notes.txt"
"$bin/download" "${local[@]}" -from notes.txt -to "$work/served.txt"
same "$work/notes.txt" "$work/served.txt"

printf '\nall examples worked\n'
