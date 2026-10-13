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

- **Client** copies a single file to or from a remote host over an SSH connection
  you have already dialled. The other end is the remote host's own `scp` program,
  so nothing needs installing there and nothing runs locally but Go.
- **Receive** and **Send** are the other end, for an SSH server that handles
  transfers itself instead of running `scp`.

Its only dependency is `golang.org/x/crypto`.

## Client

```go
conn, err := ssh.Dial("tcp", "host:22", config) // host key checking is config's job
if err != nil {
	return err
}
defer conn.Close()

c := scp.NewClient(conn)

data, err := c.ReadFile("/etc/hostname")
err = c.WriteFile("/tmp/notes.txt", []byte("hello\n"), 0o644)

// Or streamed:
header, err := c.Download("/var/log/big.log", w)
err = c.Upload("/tmp/big.bin", r, size, 0o600)
```

A failure the remote `scp` reports comes back as a `*scp.RemoteError` with its own
message, for example `scp: /etc/nope: No such file or directory`. When `scp`
cannot start at all, whatever the remote wrote to stderr is added to the error.

`Client.MaxSize` refuses a download that declares more bytes, before any of it is
transferred. `Client.Program` replaces the remote command, for a host whose `scp`
is not on the path.

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

When the function given to `Receive` returns an error, the peer is told the
transfer failed, so a sender that discards its copy on success keeps it. Return a
`*scp.Rejection` to choose the message the peer sees.

## Features

The goal is everything OpenSSH's `scp` command does with the classic protocol.
OpenSSH's own `scp` client now uses SFTP by default; `scp -O` still speaks the
classic protocol, and every `scp` still answers it as the remote end. The tests
run both halves against OpenSSH's `scp` wherever it is installed.

### Transfers

| `scp` feature | Status | Notes |
| --- | --- | --- |
| Copy one file to a remote host | ✅ Supported | `Client.Upload`, `Client.WriteFile` |
| Copy one file from a remote host | ✅ Supported | `Client.Download`, `Client.ReadFile` |
| Serve `scp -t` (receive) | ✅ Supported | `Receive` |
| Serve `scp -f` (send) | ✅ Supported | `Send`, `SendFailure` |
| Paths with spaces, quotes or shell characters | ✅ Supported | quoted as one word for the remote shell |
| Remote errors in the remote's own words | ✅ Supported | `*RemoteError` |
| Size limit on downloads and received files | ✅ Supported | `Client.MaxSize`, `Receive`'s `maxSize` (not an `scp` option) |
| Different remote `scp` path, or `sudo scp` | ✅ Supported | `Client.Program` |
| Cancellation with a context | 🚧 Planned | every transfer will take a `context.Context` |
| `-p` preserve times and modes | 🚧 Planned | received timestamp lines are acknowledged and ignored today |
| `-r` recursive copy of directories | 🚧 Planned | client and server |
| Several files in one transfer | 🚧 Planned | `scp a b host:dir/` |
| Remote wildcards (`host:'logs/*.txt'`) | 🚧 Planned | |
| File name checking (`-T` to relax it) | 🚧 Planned | a remote may only send the names that were asked for |
| Copy local paths, as the command does | 🚧 Planned | on top of the stream API |
| `-l` bandwidth limit | 🚧 Planned | |
| `-3` remote to remote, through this host | 🚧 Planned | two connections, piped |
| `-R` remote to remote, run on the first host | 🚧 Planned | |
| `-s`, `-D`, `-X` SFTP mode | ❌ Not supported | this library is the classic protocol only |
| `-C` compression | ❌ Not supported | `golang.org/x/crypto/ssh` has no compression |

Progress reporting needs no option: transfers stream through `io.Reader` and
`io.Writer`, so wrap the one you pass to count bytes.

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
| `-A` | not needed for a copy; it matters only for `-R`, which is planned |
| `-S program` | nothing to set: no `ssh` program is run |
| `-B` | `x/crypto/ssh` never prompts; nothing to set |
| `-q`, `-v` | no output of its own; errors carry the detail |

## License

MIT, see [LICENSE](LICENSE).
