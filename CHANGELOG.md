# Changelog

## v0.3.0 — 2026-10-13

- Every client transfer takes a context; cancelling it closes the session.
- `Client.Preserve` runs the remote scp with -p: uploads send times and have the
  mode reapplied to an existing file, downloads return the remote file's times.
- `Header` carries `ModTime` and `AccessTime`; `Receive` passes received times on
  and `Send` sends them ahead of the file.
- `Upload` takes a `Header` instead of a size and a mode. Breaking: every client
  method now takes a context as its first argument.

## v0.1.0 — 2026-10-13

- Client: read and write a single file on a remote host over an x/crypto/ssh
  connection, with the remote scp program as the other end.
- Receive and Send: serve "scp -t" and "scp -f" from an SSH server, with
  SendFailure for a fetch that cannot be served and Rejection to choose the
  message a refused sender sees.
- Tested against OpenSSH's scp in both directions.
