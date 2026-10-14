# go-scp

[![CI](https://github.com/antimatter-studios/go-scp/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/antimatter-studios/go-scp/actions/workflows/ci.yml)
[![Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/antimatter-studios/go-scp/badges/coverage.json)](https://github.com/antimatter-studios/go-scp/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/antimatter-studios/go-scp/pkg/scp.svg)](https://pkg.go.dev/github.com/antimatter-studios/go-scp/pkg/scp)
[![Go Report Card](https://goreportcard.com/badge/github.com/antimatter-studios/go-scp)](https://goreportcard.com/report/github.com/antimatter-studios/go-scp)
[![Version](https://img.shields.io/github/v/tag/antimatter-studios/go-scp?sort=semver&label=version)](https://github.com/antimatter-studios/go-scp/tags)
[![Go version](https://img.shields.io/github/go-mod/go-version/antimatter-studios/go-scp)](go.mod)
[![License: MIT](https://img.shields.io/github/license/antimatter-studios/go-scp)](LICENSE)

Both ends of the classic scp file-copy protocol in Go, over
[`golang.org/x/crypto/ssh`](https://pkg.go.dev/golang.org/x/crypto/ssh).

```
go get github.com/antimatter-studios/go-scp
```

```go
import "github.com/antimatter-studios/go-scp/pkg/scp"
```

- **Client** copies files and directories to and from a remote host over an SSH
  connection you have already dialled, and between two remote hosts. The other
  end is the remote host's own `scp` program, so nothing needs installing there
  and nothing runs locally but Go.
- **Receive**, **ReceiveAll**, **Send** and **SendFS** are the other end, for an
  SSH server that handles transfers itself instead of running `scp`.

Its only dependency is `golang.org/x/crypto`.

### SFTP

Copying over SFTP, as `scp -s` does, is a module of its own, so a program that
uses only `pkg/scp` does not depend on SFTP code:

```
go get github.com/antimatter-studios/go-scp/pkg/sftp
```

```go
import "github.com/antimatter-studios/go-scp/pkg/sftp"
```

`sftp.Client` has the same `CopyTo` and `CopyFrom`, with the same
`scp.Options` (`Recursive`, `Preserve`, `Glob`, `Limit`, `MaxSize` and
`Progress`), and reaches hosts that offer only the SFTP subsystem, with no shell
and no `scp` program. `sftp.Auto` uses SFTP when the host offers it and the
classic protocol when it does not; set the options once, on `Auto`. It is built on
[`github.com/pkg/sftp`](https://github.com/pkg/sftp) (BSD-2-Clause), and needs
`golang.org/x/crypto` v0.54.0 or later.

## Client

```go
conn, err := ssh.Dial("tcp", "host:22", config) // host key checking is config's job
if err != nil {
	return err
}
defer conn.Close()

c := scp.NewClient(conn)

data, err := c.ReadFile(ctx, "/etc/hostname")
err = c.WriteFile(ctx, "/tmp/notes.txt", []byte("hello\n"), 0o644)

// Or streamed:
header, err := c.Download(ctx, "/var/log/big.log", w)
err = c.Upload(ctx, "/tmp/big.bin", scp.Header{Mode: 0o600, Size: size}, r)

// Keep times and modes, as scp -p does:
c.Preserve = true
err = c.Upload(ctx, "/tmp/big.bin", scp.Header{Mode: 0o600, Size: size, ModTime: mtime}, r)
```

A failure the remote `scp` reports comes back as a `*scp.RemoteError` with its own
message, for example `scp: /etc/nope: No such file or directory`. When `scp`
cannot start at all, whatever the remote wrote to stderr is added to the error.

A copy of several files goes on past one it cannot copy. `scp.Failures` splits
its error into the files it skipped, each an `*scp.FileError` with the file's
path in the copy, and what is left, which is nil when the copy went to the end:

```go
files, rest := scp.Failures(err)
for _, f := range files {
	log.Printf("skipped %s: %v", f.Path, f.Err)
}
if rest != nil {
	return rest // the copy stopped part-way
}
```

The path is empty when only the remote's message names the file, as when the
classic protocol's remote `scp` cannot read a source; over SFTP every skipped
file is named.

Cancelling `ctx` closes the session and the transfer returns the context's error.

`Client.MaxSize` refuses a download that declares more bytes, before any of it is
transferred. `Client.Program` replaces the remote command, for a host whose `scp`
is not on the path.

### Copying paths, as the command does

```go
c.Recursive = true                                          // scp -r
err := c.CopyTo(ctx, "/srv/backup", "./project", "./notes") // scp -r project notes host:/srv/backup
err = c.CopyFrom(ctx, "./restore", "/srv/backup/project")   // scp -r host:/srv/backup/project ./restore

c.Glob = true                                               // let the remote shell expand wildcards
err = c.CopyFrom(ctx, "./logs", "/var/log/app/*.log")
```

`CopyFrom` only accepts the top-level names it asked for, as OpenSSH's `scp`
does; set `AnyNames` to relax that, as `scp -T` does. A name with a directory
part is refused whatever is set, and nothing is written outside the target.

### How the remote file is written

The remote `scp` writes over an existing file in place and then cuts it to the
new length, so the file keeps its owner and permissions. A new file is created
with the mode you pass, less the remote umask. The write is not atomic.

## Server

Give `Receive` or `Send` the stdin and stdout of a session that was asked to run
`scp -t <path>` or `scp -f <path>`:

```go
// "scp -t <path>": the peer sends a file.
err := scp.Receive(stdin, stdout, maxSize, func(h scp.Header, body io.Reader) error {
	return store(h.Name, body) // h.Name comes from the peer: never use it as a path
})

// "scp -f <path>": the peer fetches a file.
err := scp.Send(stdin, stdout, scp.Header{Mode: 0o644, Size: size, Name: "report.txt"}, body)

// A fetch you cannot serve.
err := scp.SendFailure(stdin, stdout, "scp: report.txt: No such file or directory")
```

Times a sender preserved with `-p` arrive in the header's `ModTime` and
`AccessTime`; applying them is up to you. `Send` sends a header's times ahead of
the file, so set them only when the peer ran `scp -p -f`.

To serve the whole command as `scp` would, any number of files and directories
included, hand it to `Serve` with the directory to serve, opened as an `os.Root`:

```go
root, err := os.OpenRoot("/srv/share")
if err != nil {
	return err
}
defer root.Close()

// For each session that asks to run a command:
status := scp.Serve(command, channel, channel.Stderr(), root)
// send status back as the session's exit status
```

`Serve` parses the command with `ParseCommand`, which refuses anything a shell
would run as more than one plain `scp` command, and finds every path inside
`root`: a path that climbs out with `..` is taken from `root`'s top, and a
symlink cannot lead a read or a write out of it. To put the pieces together
yourself, use `ParseCommand`, `Command.Relative`, `NewDirSinkIn` with
`ReceiveAll`, and `os.Root.FS` with `Command.Expand` and `SendFS`.
`NewDirSink` takes a path as given, symlinks and all, so keep it for paths you
chose yourself, never ones a client sent.

To say what a client may do as well as where, serve with a `Server`:

```go
share := &scp.Server{Root: root, ReadOnly: true} // clients may only fetch files
inbox := &scp.Server{Root: root, WriteOnly: true, MaxSize: 100 << 20, Atomic: true}

status := inbox.Serve(command, channel, channel.Stderr())
```

`ReadOnly` refuses uploads and `WriteOnly` downloads, each telling the client why
before anything moves. `MaxSize` refuses an uploaded file that declares more
bytes; the client skips it and carries on. `Atomic` writes each upload under a
temporary name and renames it into place once whole, so a program watching the
directory never picks up half a file.

When the function given to `Receive` returns an error, the peer is told the
transfer failed, so a sender that discards its copy on success keeps it. Return a
`*scp.Rejection` to choose the message the peer sees.

## Examples

Every feature has a small runnable program in [`examples/`](examples), linked
from the table below. Each takes the host, user and key as flags and checks the
host's key against `~/.ssh/known_hosts`:

```
go run ./examples/upload -addr host:22 -key ~/.ssh/id_ed25519 -from notes.txt -to /tmp/notes.txt
```

`scripts/examples.sh` runs all of them against the test network, and CI runs it,
so the examples keep working as the library changes.

## Features

The goal is everything OpenSSH's `scp` command does. OpenSSH's own `scp` client
now uses SFTP by default; `scp -O` still speaks the classic protocol, and every
`scp` still answers it as the remote end. `pkg/scp` is the classic protocol, and
the tests run both of its halves against OpenSSH's `scp` wherever it is
installed; `pkg/sftp` is SFTP, tested against OpenSSH's `sftp-server`.

### Transfers

| `scp` feature | Status | Notes | Example |
| --- | --- | --- | --- |
| Copy one file to a remote host | ✅ Supported | `Client.Upload`, `Client.WriteFile` | [upload](examples/upload/main.go) |
| Copy one file from a remote host | ✅ Supported | `Client.Download`, `Client.ReadFile` | [download](examples/download/main.go) |
| Serve `scp -t` (receive) | ✅ Supported | `Receive`, `ReceiveAll`, `DirSink` | [server](examples/server/main.go) |
| Serve `scp -f` (send) | ✅ Supported | `Send`, `SendFS`, `SendFailure` | [server](examples/server/main.go) |
| Limit what a served client may do | ✅ Supported | `Server.ReadOnly`, `Server.WriteOnly`, `Server.MaxSize` (not `scp` options) | |
| Paths with spaces, quotes or shell characters | ✅ Supported | quoted as one word for the remote shell | |
| Remote errors in the remote's own words | ✅ Supported | `*RemoteError` | |
| Size limit on downloads and received files | ✅ Supported | `Client.MaxSize`, `Receive`'s `maxSize`, `sftp.Client.MaxSize` (not an `scp` option) | |
| Different remote `scp` path, or `sudo scp` | ✅ Supported | `Client.Program` | [sudo](examples/sudo/main.go) |
| Cancellation with a context | ✅ Supported | every client transfer takes a `context.Context` | |
| `-p` preserve times and modes | ✅ Supported | `Client.Preserve`; `Header.ModTime`, `Header.AccessTime` on both halves | [preserve](examples/preserve/main.go) |
| `-r` recursive copy of directories | ✅ Supported | `Client.Recursive`; `ReceiveAll`, `SendFS` with `Recursive`. Symlinks to directories are followed, as scp does, except a link back to a directory it is inside, which is skipped and reported | [copy-tree](examples/copy-tree/main.go) |
| Several files in one transfer | ✅ Supported | `Client.CopyTo`, `Client.CopyFrom`; `ReceiveAll`, `SendFS` | [wildcards](examples/wildcards/main.go) |
| Remote wildcards (`host:'logs/*.txt'`) | ✅ Supported | `Client.Glob`; servers expand with `Command.Expand` | [wildcards](examples/wildcards/main.go) |
| File name checking (`-T` to relax it) | ✅ Supported | on by default; `Client.AnyNames` relaxes it; names with a directory part are always refused | [wildcards](examples/wildcards/main.go) |
| Copy local paths, as the command does | ✅ Supported | `Client.CopyTo`, `Client.CopyFrom`, `DirSink` | [copy-tree](examples/copy-tree/main.go) |
| `-l` bandwidth limit | ✅ Supported | `Client.Limit` and `sftp.Client.Limit`, in bytes per second; `scp -l n` (Kbit/s) is `n*1000/8` | [bandwidth](examples/bandwidth/main.go) |
| `-3` remote to remote, through this host | ✅ Supported | `CopyBetween`, and `sftp.CopyBetween` over SFTP; each file is read from one host and written to the other, with name checks and per-file errors | [between-hosts](examples/between-hosts/main.go), [sftp](examples/sftp/main.go) |
| `-R` remote to remote, run on the first host | ✅ Supported | `Client.CopyOnHost`; the first host's own scp and SSH setup reach the second | [on-host](examples/on-host/main.go) |
| `-A` agent forwarding | ✅ Supported | `Client.ForwardAgent`, after `agent.ForwardToAgent` on the connection; lets `-R` reach the second host with your keys | [on-host](examples/on-host/main.go) |
| `-s` SFTP mode | ✅ Supported | `sftp.NewClient`, in the separate `pkg/sftp` module | [sftp](examples/sftp/main.go) |
| `-D` a local SFTP server program | ✅ Supported | `sftp.NewProgramClient` | [sftp](examples/sftp/main.go) |
| `-X` SFTP options | ✅ Supported | `sftp.Client.Requests` (`nrequests`), `sftp.Client.BufferSize` (`buffer`) | |
| Several files at once over SFTP | ✅ Supported | `sftp.Client.Parallel` (not an `scp` option): many small files copied about 2.5 times as fast with 8 at once on loopback | |
| SFTP or the classic protocol, whichever the host offers | ✅ Supported | `sftp.Auto` (not an `scp` option) | [sftp](examples/sftp/main.go) |
| Files that appear whole or not at all | ✅ Supported | `Atomic`: each file is written under a temporary name beside its target and renamed into place once complete (not an `scp` option). Honoured wherever this library writes: downloads, `DirSink` and, over SFTP, uploads; classic uploads refuse it, because the remote `scp` writes in place | |
| Resume a copy left part-way | ✅ Supported | `sftp.Client.Resume`, as sftp's `reget` and `reput` do (not an `scp` option; the classic protocol always sends a file whole) | |
| `-C` compression | ❌ Not supported | `golang.org/x/crypto/ssh` has no compression, and adding it there waits on partial flush in `compress/flate` ([golang/go#31514](https://github.com/golang/go/issues/31514)) | |

Progress needs no `scp` option: set `Progress` on either client, or on
`sftp.Auto`, and it is told as each file of every transfer starts and as its
bytes move, with the file's path in the transfer and its size. See [progress](examples/progress/main.go).

### Connection options

These `scp` options configure the SSH connection, not the copy. Here the
connection is the `*ssh.Client` you pass to `NewClient`, so they are set where
it is dialled:

| `scp` option | Where it goes |
| --- | --- |
| `-P port` | the address given to `ssh.Dial` |
| `-i identity_file` | `ssh.PublicKeys` in `ssh.ClientConfig.Auth` |
| `-o option`, `-F ssh_config` | the matching `ssh.ClientConfig` field |
| `-c cipher` | `ssh.ClientConfig.Ciphers` |
| `-J destination` | dial the jump host, then `Dial` through it and `ssh.NewClientConn` |
| `-4`, `-6` | the network given to `ssh.Dial` (`tcp4`, `tcp6`) |
| `-A` | `agent.ForwardToAgent(conn, keyring)` once on the connection, then `Client.ForwardAgent` |
| `-S program` | nothing to set: no `ssh` program is run |
| `-B` | `x/crypto/ssh` never prompts; nothing to set |
| `-q`, `-v` | no output of its own; errors carry the detail |

## Windows

The library builds and is tested on Windows, with these differences:

- Windows keeps only a read-only flag, not Unix permission bits, so modes sent
  or preserved come out as Windows can hold them.
- A received name holding `\` or `:` is refused, since on Windows either is a
  directory part or a drive.
- The access time sent with `-p` comes from the file's Windows attributes.

## Testing

Four layers, all run by CI:

- **Unit tests** (`go test ./...`): both halves against each other over an
  in-process SSH server, and the wire format byte for byte.
- **Fuzzing** (`scripts/fuzz.sh`): every part that reads what the other end
  sends, fed arbitrary input: both ends of a transfer, the client's download,
  the command parser and the time and header lines. Each must finish, never
  write outside its target and never write more than arrived. The seeds and
  every input that once failed (`pkg/scp/testdata/fuzz`) run with the unit
  tests; CI also fuzzes each target for a short while.
- **OpenSSH interoperability**: the client against OpenSSH's `scp` as the remote
  end, and OpenSSH's `scp -O` client against the server half. Part of the unit
  tests; they run wherever `scp` is installed.
- **Integration** (`scripts/integration.sh`, needs Docker): two `sshd`
  containers on one network (`test/network`). Every remote command goes through
  a real login shell, so quoting, wildcards and `sudo scp` are tested as they
  run in production.

CI also fails unless the tests cover every statement in `pkg/`
(`scripts/coverage-gate.sh`), naming whatever they miss.

## License

MIT, see [LICENSE](LICENSE).
