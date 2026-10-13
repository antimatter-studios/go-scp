# Changelog

## Unreleased

- SFTP downloads ask for a whole read-ahead at once, `Requests` packets of
  `BufferSize` bytes, so that many reads are in flight together: a download
  over a link with 1 ms of latency went from 24 to 192 MB/s in the benchmark.
- With -p the access time sent is the file's own, read from its stat on Linux
  and macOS, rather than its modification time; `AccessTime` returns it, and
  SFTP copies carry it both ways.
- `Client.Progress`, and `Progress` in `SendOptions` and `ReceiveOptions`, is
  told as each file of a transfer starts and as its bytes move, for single
  files, trees and copies between hosts alike.
- `Serve` answers one scp command for an SSH server under an `os.Root`,
  confined to it through symlinks, and `NewDirSinkIn` opens a `DirSink` for a
  path inside a root. The server example, the package example and the README
  used `NewDirSink` on a path joined onto the served directory, which a
  symlink inside that directory could lead a write out of; they now use
  `Serve`.
- `sftp.Client.Limit` paces SFTP copies in bytes per second, as `scp -l` does.
- `sftp.Client.MaxSize` refuses a remote file larger than it, as
  `scp.Client.MaxSize` does for classic downloads.
- `sftp.CopyBetween` copies between two hosts through this program over SFTP,
  as `scp -3` does.

## v0.6.0 — 2026-10-13

- SFTP, as `scp -s` does, in the separate module
  `github.com/antimatter-studios/go-scp/pkg/sftp`, released as
  `pkg/sftp/v0.1.0` and built on
  `github.com/pkg/sftp`: `sftp.Client` with `CopyTo` and `CopyFrom`,
  `NewProgramClient` for a local server program (`-D`), `Requests` and
  `BufferSize` (`-X`), and `Auto`, which falls back to the classic protocol
  when a host offers no SFTP.
- Uploads over SFTP copy through plain writes, since `github.com/pkg/sftp`'s
  `File.ReadFrom` can report a file whole when the server refused its last
  part.
- `Client.ForwardAgent` forwards the caller's agent, as `scp -A` does, so
  `CopyOnHost` can reach the second host with the caller's keys.
- Every remote command ends scp's options with `--`, so a path that starts with
  a dash is no longer taken for an option.
- Protocol lines are read up to 4096 bytes, so a peer that never sends a
  newline can no longer make memory grow without end; a longer failure message
  is cut there.
- `DirSink` keeps only the bytes that arrived when a file body ends short,
  instead of padding the file to its declared size, and fails that file.
- `DirSink` holds each directory it enters open and caps paths under its target
  at 4096 bytes, so deep nesting costs the same per level and cannot go on
  without end.
- `ParseCommand` reads a leading unquoted `~` as the login directory, as the
  remote shell would for OpenSSH's scp client.
- Fuzz targets for every part that reads what the other end sends, run briefly
  in CI.

## v0.5.0 — 2026-10-13

- `Client.Limit` caps a client transfer's rate in bytes per second, as `scp -l`
  does.
- `CopyBetween` copies between two remote hosts through this program, as
  `scp -3` does: each file is read from one host and written to the other, with
  name checks and per-file errors.
- `Client.CopyOnHost` runs scp on the connected host to copy to another host, as
  `scp -R` does.
- `examples/` has a runnable program for every feature, linked from the README.
- CI tests against two `sshd` containers on one network, runs every example
  against them, and fails unless the tests cover every statement in `pkg/`.

## v0.4.0 — 2026-10-13

- `Client.CopyTo` and `Client.CopyFrom` copy local paths to and from a remote
  host, as the `scp` command does: several files at once, and directories with
  `Client.Recursive` (`-r`).
- `Client.Glob` leaves wildcards in remote sources for the remote shell.
- `CopyFrom` accepts only the top-level names it asked for unless
  `Client.AnyNames` is set (`-T`), and never a name with a directory part.
- `ReceiveAll` and `SendFS` serve `scp -t` and `scp -f` for any number of files
  and directories; a failed file is reported and the transfer carries on.
- `DirSink` writes under one target through an `os.Root`.
- `ParseCommand`, `Command.Relative` and `Command.Expand` read the command a
  session asks to run.

## v0.3.0 — 2026-10-13

- Every client transfer takes a context; cancelling it closes the session.
- `Client.Preserve` runs the remote scp with -p: uploads send times and have the
  mode reapplied to an existing file, downloads return the remote file's times.
- `Header` carries `ModTime` and `AccessTime`; `Receive` passes received times on
  and `Send` sends them ahead of the file.
- `Upload` takes a `Header` instead of a size and a mode. Breaking: every client
  method now takes a context as its first argument.

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
