# go-scp

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

## Scope

Single regular files only: no recursive copies and no directories. Timestamp
lines (`scp -p`) are acknowledged and ignored when received, and never sent.

OpenSSH has moved its own `scp` client to SFTP by default; `scp -O` still speaks
this protocol, and every `scp` still answers it as the remote end. The tests run
both halves against OpenSSH's `scp` wherever it is installed.

## License

MIT, see [LICENSE](LICENSE).
