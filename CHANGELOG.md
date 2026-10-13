# Changelog

## v0.1.0 — 2026-10-13

- Client: read and write a single file on a remote host over an x/crypto/ssh
  connection, with the remote scp program as the other end.
- Receive and Send: serve "scp -t" and "scp -f" from an SSH server, with
  SendFailure for a fetch that cannot be served and Rejection to choose the
  message a refused sender sees.
- Tested against OpenSSH's scp in both directions.
