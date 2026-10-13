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
	conn *ssh.Client

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
}

// NewClient returns a Client that makes its transfers over conn. Verifying the
// remote host's key is conn's business, settled when it was dialled.
func NewClient(conn *ssh.Client) *Client {
	return &Client{conn: conn}
}

// Download copies the regular file at remotePath to w, and returns its header.
func (c *Client) Download(ctx context.Context, remotePath string, w io.Writer) (Header, error) {
	var h Header
	err := c.run(ctx, "-f", remotePath, func(r *bufio.Reader, out io.Writer) error {
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
	return c.run(ctx, "-t", remotePath, func(r *bufio.Reader, out io.Writer) error {
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

// run starts scp on the remote host for remotePath and holds one exchange with it.
// When the exchange fails the session is closed at once, so a remote scp that is
// still writing cannot leave the caller waiting; anything it wrote to stderr is
// added to the error, which is usually where a missing scp explains itself.
// Cancelling ctx closes the session, which ends the exchange; the error is then
// ctx's.
func (c *Client) run(ctx context.Context, flag, remotePath string, exchange func(*bufio.Reader, io.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sess, err := c.conn.NewSession()
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

	program := c.Program
	if program == "" {
		program = "scp"
	}
	if c.Preserve {
		flag = "-p " + flag
	}
	if err := sess.Start(program + " " + flag + " " + Quote(remotePath)); err != nil {
		return err
	}

	if err := exchange(bufio.NewReader(stdout), stdin); err != nil {
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
