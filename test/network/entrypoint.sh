#!/bin/sh
# Installs the test key for the tester user, makes host keys, and runs sshd in
# the foreground.
set -eu
install -d -m 700 -o tester -g tester /home/tester/.ssh
install -m 600 -o tester -g tester /keys/id_ed25519.pub /home/tester/.ssh/authorized_keys
install -m 600 -o tester -g tester /keys/id_ed25519 /home/tester/.ssh/id_ed25519
ssh-keygen -A
exec /usr/sbin/sshd -D -e
