# Changelog

## v0.2.0 — 2026-10-13

- The package moves from the module root to pkg/scp; import
  github.com/antimatter-studios/go-scp/pkg/scp. v0.1.0 is retracted.

## v0.1.0 — 2026-10-13

- Client: read and write a single file on a remote host over an x/crypto/ssh
  connection, with the remote scp program as the other end.
- Receive and Send: serve "scp -t" and "scp -f" from an SSH server, with
  SendFailure for a fetch that cannot be served and Rejection to choose the
  message a refused sender sees.
- Tested against OpenSSH's scp in both directions.
