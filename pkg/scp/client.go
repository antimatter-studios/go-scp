package scp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Client copies files and directories to and from a remote host over an SSH
// connection, with the remote host's scp program as the other end of each
// transfer. Each transfer opens its own session on the connection, so one Client
// serves any number of them, one after another or at once.
type Client struct {
	// newSession opens a session on the connection. A field so a test can give the
	// client scripted sessions that fail where a live one would not.
	newSession func() (session, error)

	// Program is the command that starts scp on the remote host; "scp" when empty.
	// It is sent as written, so a path in it must already be quoted.
	Program string

	// MaxSize, when positive, refuses a download that declares more bytes, before
	// any of the file is transferred.
	MaxSize int64

	// Preserve keeps file times and modes, as "scp -p" does: an upload sends the
	// header's times and the remote reapplies the mode to an existing file; a
	// download returns the remote file's times in its header.
	Preserve bool

	// Recursive lets CopyTo and CopyFrom copy directories with their contents, as
	// "scp -r" does.
	Recursive bool

	// Glob lets the remote sources given to CopyFrom hold wildcards (*, ? and
	// [...]) for the remote shell to expand, as scp's remote paths do. The rest of
	// each source stays quoted.
	Glob bool

	// AnyNames lets CopyFrom accept top-level names other than the ones asked
	// for, as "scp -T" does. A name that could reach outside the target is refused
	// whatever this says.
	AnyNames bool

	// Limit, when positive, caps each transfer at that many bytes per second, both
	// directions together, as "scp -l" does. scp's -l counts Kbit/s, so -l n is a
	// Limit of n*1000/8.
	Limit int64

	// Progress, when set, is told how each file of every transfer moves: as it
	// starts and as its bytes go, with its path in the transfer and its size.
	Progress Progress

	// ForwardAgent asks every session for agent forwarding, as "scp -A" does, so
	// that the host CopyOnHost runs scp on can log in to the other host with the
	// caller's keys. Register the agent on the connection first, once, with
	// agent.ForwardToAgent(conn, keyring): x/crypto/ssh takes one agent handler
	// per connection. A host that refuses forwarding fails the transfer.
	ForwardAgent bool
}

// session is the part of *ssh.Session a transfer uses.
type session interface {
	SendRequest(name string, wantReply bool, payload []byte) (bool, error)
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.Reader, error)
	StderrPipe() (io.Reader, error)
	Start(cmd string) error
	Wait() error
	Close() error
}

// NewClient returns a Client that makes its transfers over conn. Verifying the
// remote host's key is conn's business, settled when it was dialled.
func NewClient(conn *ssh.Client) *Client {
	return &Client{newSession: func() (session, error) { return conn.NewSession() }}
}

// openSession opens a session for one transfer, with agent forwarding when
// ForwardAgent is set.
func (c *Client) openSession() (session, error) {
	sess, err := c.newSession()
	if err != nil {
		return nil, err
	}
	if c.ForwardAgent {
		ok, err := sess.SendRequest("auth-agent-req@openssh.com", true, nil)
		if err == nil && !ok {
			err = errors.New("scp: the remote host refused to forward the agent")
		}
		if err != nil {
			sess.Close()
			return nil, err
		}
	}
	return sess, nil
}

// Download copies the regular file at remotePath to w, and returns its header.
func (c *Client) Download(ctx context.Context, remotePath string, w io.Writer) (Header, error) {
	file := &oneFile{take: func(h Header, body io.Reader) error {
		_, err := io.Copy(w, body)
		return err
	}}
	err := c.run(ctx, c.command("-f", Quote(remotePath)), func(r *bufio.Reader, out io.Writer) error {
		return file.receive(r, out, ReceiveOptions{MaxSize: c.MaxSize, Progress: c.Progress})
	})
	return file.h, err
}

// ReadFile returns the contents of the regular file at remotePath.
func (c *Client) ReadFile(ctx context.Context, remotePath string) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := c.Download(ctx, remotePath, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Upload copies h.Size bytes read from body to remotePath, creating the file with
// h.Mode, less the remote umask, when it does not exist. An existing file is
// written over in place and cut to the new length, keeping its owner and
// permissions; that is what the remote scp does, and it is not atomic. With
// Preserve, the remote also applies h.Mode to an existing file, and h.ModTime and
// h.AccessTime when they are set. h.Name is not used: the name is remotePath's.
func (c *Client) Upload(ctx context.Context, remotePath string, h Header, body io.Reader) error {
	h.Mode = h.Mode.Perm()
	h.Name = path.Base(remotePath)
	if err := validName(h.Name); err != nil {
		return err
	}
	if !c.Preserve {
		h.ModTime, h.AccessTime = time.Time{}, time.Time{}
	}
	return c.run(ctx, c.command("-t", Quote(remotePath)), func(r *bufio.Reader, out io.Writer) error {
		// The receiver is ready.
		if err := readStatus(r); err != nil {
			return err
		}
		failure, err := sender{r: r, out: out, progress: c.Progress}.file(h, body, remotePath)
		if err != nil {
			return err
		}
		return failure
	})
}

// WriteFile writes data to remotePath; see Upload for how the file is written.
func (c *Client) WriteFile(ctx context.Context, remotePath string, data []byte, mode os.FileMode) error {
	return c.Upload(ctx, remotePath, Header{Mode: mode, Size: int64(len(data))}, bytes.NewReader(data))
}

// command returns the remote scp command line: the program, -p when preserving,
// any extra flags, then -- and the operands, already quoted. Quoting keeps the
// remote shell from splitting a path, but once the shell has removed the quotes
// scp's option parser would take a path such as -r for an option; -- ends the
// options first.
func (c *Client) command(flags string, operands ...string) string {
	program := c.Program
	if program == "" {
		program = "scp"
	}
	if c.Preserve {
		flags = "-p " + flags
	}
	return program + " " + flags + " -- " + strings.Join(operands, " ")
}

// run starts command on the remote host and holds one exchange with its scp.
// When the exchange fails the session is closed at once, so a remote scp that is
// still writing cannot leave the caller waiting; anything it wrote to stderr is
// added to the error, which is usually where a missing scp explains itself.
// Cancelling ctx closes the session, which ends the exchange; the error is then
// ctx's.
func (c *Client) run(ctx context.Context, command string, exchange func(*bufio.Reader, io.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := c.start(ctx, command)
	if err != nil {
		return err
	}
	defer p.close()

	var in io.Reader = p.stdout
	var out io.Writer = p.stdin
	if c.Limit > 0 {
		pace := newLimiter(c.Limit)
		in, out = limitedReader{r: p.stdout, l: pace}, limitedWriter{w: p.stdin, l: pace}
	}

	if err := exchange(bufio.NewReader(in), out); err != nil {
		p.sess.Close()
		<-p.stderrDone
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		var remote *RemoteError
		if !errors.As(err, &remote) {
			if msg := strings.TrimSpace(p.stderr.String()); msg != "" {
				err = fmt.Errorf("%w (remote: %s)", err, msg)
			}
		}
		return err
	}

	// End of input, so the remote scp finishes and exits. It may have done so
	// already, which makes closing fail harmlessly: its exit status is the verdict.
	p.stdin.Close()
	if err := p.wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	return nil
}
