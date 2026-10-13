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

	"golang.org/x/crypto/ssh"
)

// Client copies single files to and from a remote host over an SSH connection, with
// the remote host's scp program as the other end of each transfer. Each transfer
// opens its own session on the connection, so one Client serves any number of them,
// one after another or at once.
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
	var h Header
	err := c.run(ctx, c.command("-f", Quote(remotePath)), func(r *bufio.Reader, out io.Writer) error {
		// Ready for the file.
		if err := writeOK(out); err != nil {
			return err
		}
		var err error
		if h, err = downloadHeader(r, out); err != nil {
			return err
		}
		if c.MaxSize > 0 && h.Size > c.MaxSize {
			_ = writeFailure(out, "file too large")
			return fmt.Errorf("scp: %s is %d bytes, more than the %d this client accepts", remotePath, h.Size, c.MaxSize)
		}
		// Header accepted; send the body.
		if err := writeOK(out); err != nil {
			return err
		}
		if _, err := io.CopyN(w, r, h.Size); err != nil {
			return fmt.Errorf("scp: reading %s: %w", remotePath, err)
		}
		// The sender reports whether it read the whole file.
		if err := readStatus(r); err != nil {
			return err
		}
		return writeOK(out)
	})
	return h, err
}

// ReadFile returns the contents of the regular file at remotePath.
func (c *Client) ReadFile(ctx context.Context, remotePath string) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := c.Download(ctx, remotePath, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// downloadHeader reads up to the file's control line, keeping the times from a time
// line on the way.
func downloadHeader(r *bufio.Reader, out io.Writer) (Header, error) {
	var times Header
	for {
		kind, err := r.ReadByte()
		if err != nil {
			return Header{}, fmt.Errorf("%w: expected a control line: %v", ErrProtocol, err)
		}
		switch kind {
		case 'C':
			line, err := r.ReadString('\n')
			if err != nil {
				return Header{}, fmt.Errorf("%w: reading a control line: %v", ErrProtocol, err)
			}
			h, err := parseHeader("C" + strings.TrimRight(line, "\n"))
			h.ModTime, h.AccessTime = times.ModTime, times.AccessTime
			return h, err
		case 'T':
			line, err := r.ReadString('\n')
			if err != nil {
				return Header{}, fmt.Errorf("%w: reading a time line: %v", ErrProtocol, err)
			}
			if err := parseTimes("T"+strings.TrimRight(line, "\n"), &times); err != nil {
				return Header{}, err
			}
			if err := writeOK(out); err != nil {
				return Header{}, err
			}
		case 'D':
			return Header{}, fmt.Errorf("%w: a directory was sent where a file was asked for", ErrProtocol)
		default:
			if err := checkStatus(r, kind); err != nil {
				return Header{}, err
			}
			return Header{}, fmt.Errorf("%w: success where a control line belongs", ErrProtocol)
		}
	}
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
	return c.run(ctx, c.command("-t", Quote(remotePath)), func(r *bufio.Reader, out io.Writer) error {
		// The receiver is ready.
		if err := readStatus(r); err != nil {
			return err
		}
		if c.Preserve && h.hasTimes() {
			if _, err := io.WriteString(out, h.timesLine()+"\n"); err != nil {
				return err
			}
			if err := readStatus(r); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(out, h.String()+"\n"); err != nil {
			return err
		}
		// It has opened the file and waits for the body.
		if err := readStatus(r); err != nil {
			return err
		}
		if n, err := io.CopyN(out, body, h.Size); err != nil {
			return fmt.Errorf("scp: reading the upload for %s after %d of %d bytes: %w", remotePath, n, h.Size, err)
		}
		if err := writeOK(out); err != nil {
			return err
		}
		// And reports whether it stored it.
		return readStatus(r)
	})
}

// WriteFile writes data to remotePath; see Upload for how the file is written.
func (c *Client) WriteFile(ctx context.Context, remotePath string, data []byte, mode os.FileMode) error {
	return c.Upload(ctx, remotePath, Header{Mode: mode, Size: int64(len(data))}, bytes.NewReader(data))
}

// command returns the remote scp command line: the program, -p when preserving,
// any extra flags, then the operands, already quoted.
func (c *Client) command(flags string, operands ...string) string {
	program := c.Program
	if program == "" {
		program = "scp"
	}
	if c.Preserve {
		flags = "-p " + flags
	}
	return program + " " + flags + " " + strings.Join(operands, " ")
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
	sess, err := c.openSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	stop := context.AfterFunc(ctx, func() { sess.Close() })
	defer stop()

	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	stderrPipe, err := sess.StderrPipe()
	if err != nil {
		return err
	}
	// Collected by this goroutine alone; read only once it has finished, which it
	// does when the session ends.
	var stderr bytes.Buffer
	stderrDone := make(chan struct{})
	go func() {
		io.Copy(&stderr, stderrPipe)
		close(stderrDone)
	}()

	if err := sess.Start(command); err != nil {
		return err
	}

	var in io.Reader = stdout
	var out io.Writer = stdin
	if c.Limit > 0 {
		pace := newLimiter(c.Limit)
		in, out = limitedReader{r: stdout, l: pace}, limitedWriter{w: stdin, l: pace}
	}

	if err := exchange(bufio.NewReader(in), out); err != nil {
		sess.Close()
		<-stderrDone
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		var remote *RemoteError
		if !errors.As(err, &remote) {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				err = fmt.Errorf("%w (remote: %s)", err, msg)
			}
		}
		return err
	}

	// End of input, so the remote scp finishes and exits. It may have done so
	// already, which makes closing fail harmlessly: its exit status is the verdict.
	stdin.Close()
	if err := sess.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	return nil
}
