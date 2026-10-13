package scp

import (
	"bufio"
	"bytes"
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
}

// NewClient returns a Client that makes its transfers over conn. Verifying the
// remote host's key is conn's business, settled when it was dialled.
func NewClient(conn *ssh.Client) *Client {
	return &Client{conn: conn}
}

// Download copies the regular file at remotePath to w, and returns its header.
func (c *Client) Download(remotePath string, w io.Writer) (Header, error) {
	var h Header
	err := c.run("-f", remotePath, func(r *bufio.Reader, out io.Writer) error {
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
func (c *Client) ReadFile(remotePath string) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := c.Download(remotePath, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// downloadHeader reads up to the file's control line, acknowledging timestamp lines
// on the way.
func downloadHeader(r *bufio.Reader, out io.Writer) (Header, error) {
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
			return parseHeader("C" + strings.TrimRight(line, "\n"))
		case 'T':
			if _, err := r.ReadString('\n'); err != nil {
				return Header{}, fmt.Errorf("%w: reading a timestamp line: %v", ErrProtocol, err)
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

// Upload copies size bytes read from body to remotePath, creating the file with
// mode, less the remote umask, when it does not exist. An existing file is written
// over in place and cut to the new length, keeping its owner and permissions; that
// is what the remote scp does, and it is not atomic.
func (c *Client) Upload(remotePath string, body io.Reader, size int64, mode os.FileMode) error {
	h := Header{Mode: mode.Perm(), Size: size, Name: path.Base(remotePath)}
	if err := validName(h.Name); err != nil {
		return err
	}
	return c.run("-t", remotePath, func(r *bufio.Reader, out io.Writer) error {
		// The receiver is ready.
		if err := readStatus(r); err != nil {
			return err
		}
		if _, err := io.WriteString(out, h.String()+"\n"); err != nil {
			return err
		}
		// It has opened the file and waits for the body.
		if err := readStatus(r); err != nil {
			return err
		}
		if n, err := io.CopyN(out, body, size); err != nil {
			return fmt.Errorf("scp: reading the upload for %s after %d of %d bytes: %w", remotePath, n, size, err)
		}
		if err := writeOK(out); err != nil {
			return err
		}
		// And reports whether it stored it.
		return readStatus(r)
	})
}

// WriteFile writes data to remotePath; see Upload for how the file is written.
func (c *Client) WriteFile(remotePath string, data []byte, mode os.FileMode) error {
	return c.Upload(remotePath, bytes.NewReader(data), int64(len(data)), mode)
}

// run starts scp on the remote host for remotePath and holds one exchange with it.
// When the exchange fails the session is closed at once, so a remote scp that is
// still writing cannot leave the caller waiting; anything it wrote to stderr is
// added to the error, which is usually where a missing scp explains itself.
func (c *Client) run(flag, remotePath string, exchange func(*bufio.Reader, io.Writer) error) error {
	sess, err := c.conn.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

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
	if err := sess.Start(program + " " + flag + " " + Quote(remotePath)); err != nil {
		return err
	}

	if err := exchange(bufio.NewReader(stdout), stdin); err != nil {
		sess.Close()
		<-stderrDone
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
	return sess.Wait()
}
