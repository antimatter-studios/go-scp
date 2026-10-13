package scp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Rejection fails a received transfer with a message of the caller's choosing:
// the peer is told Message, and Receive returns the Rejection, which unwraps to Err.
// Without it the peer is told the error's own text.
type Rejection struct {
	Message string
	Err     error
}

func (r *Rejection) Error() string { return r.Err.Error() }
func (r *Rejection) Unwrap() error { return r.Err }

// Receive serves the receiving end of a transfer, as "scp -t" does: the peer sends
// one file. in and out are the session's stdin and stdout.
//
// accept is handed the file's header and a reader of exactly Header.Size bytes.
// Whatever accept leaves unread is drained afterwards, so the peer's trailing
// status byte is never taken for file content. When accept returns an error the
// peer is told the transfer failed, and Receive returns that error; a sender that
// treats success as permission to discard its copy therefore keeps it.
//
// maxSize, when positive, refuses a file that declares more bytes, before accept
// is called. Times the sender preserved ("scp -p") arrive in the header's ModTime
// and AccessTime; applying them is accept's business. Directories are refused.
func Receive(in io.Reader, out io.Writer, maxSize int64, accept func(Header, io.Reader) error) error {
	r := bufio.NewReader(in)

	// Ready for a control line.
	if err := writeOK(out); err != nil {
		return err
	}

	h, reply, err := receiveHeader(r, out)
	if err != nil {
		if reply != "" {
			// The peer waits for a status byte; without one it reports a
			// confusing error of its own.
			_ = writeFailure(out, reply)
		}
		return err
	}

	if maxSize > 0 && h.Size > maxSize {
		_ = writeFailure(out, "file too large")
		return fmt.Errorf("scp: %q declares %d bytes, more than the %d accepted", h.Name, h.Size, maxSize)
	}

	// Header accepted; the body comes next.
	if err := writeOK(out); err != nil {
		return err
	}

	body := io.LimitReader(r, h.Size)
	acceptErr := accept(h, body)
	if _, err := io.Copy(io.Discard, body); err != nil && acceptErr == nil {
		acceptErr = err
	}

	// The sender reports its own view of the transfer before hearing ours.
	status, err := r.ReadByte()
	if err != nil {
		if acceptErr != nil {
			return acceptErr
		}
		return fmt.Errorf("%w: no status byte after the body: %v", ErrProtocol, err)
	}
	if senderErr := checkStatus(r, status); senderErr != nil && acceptErr == nil {
		acceptErr = senderErr
	}

	if acceptErr != nil {
		_ = writeFailure(out, failureMessage(acceptErr))
		return acceptErr
	}
	return writeOK(out)
}

// receiveHeader reads up to the file's control line, keeping the times from a time
// line on the way. On failure it also returns the message to send the peer, or ""
// when the peer reported the failure itself and expects no answer.
func receiveHeader(r *bufio.Reader, out io.Writer) (Header, string, error) {
	var times Header
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return Header{}, "malformed control line", fmt.Errorf("%w: reading a control line: %v", ErrProtocol, err)
		}
		line = strings.TrimRight(line, "\n")
		if line == "" {
			return Header{}, "malformed control line", fmt.Errorf("%w: empty control line", ErrProtocol)
		}

		switch line[0] {
		case 'T':
			if err := parseTimes(line, &times); err != nil {
				return Header{}, "malformed time line", err
			}
			if err := writeOK(out); err != nil {
				return Header{}, "", err
			}
		case 'C':
			h, err := parseHeader(line)
			if err != nil {
				return Header{}, "malformed control line", err
			}
			h.ModTime, h.AccessTime = times.ModTime, times.AccessTime
			return h, "", nil
		case 'D', 'E':
			return Header{}, "directories are not supported", fmt.Errorf("%w: directories are not supported", ErrProtocol)
		case statusError, statusFatal:
			return Header{}, "", &RemoteError{Message: line[1:], Fatal: line[0] == statusFatal}
		default:
			return Header{}, "malformed control line", fmt.Errorf("%w: unexpected control line %q", ErrProtocol, line)
		}
	}
}

// failureMessage is what the peer is told about err.
func failureMessage(err error) string {
	var rejection *Rejection
	if errors.As(err, &rejection) {
		return rejection.Message
	}
	return err.Error()
}

// Send serves the sending end of a transfer, as "scp -f" does: the peer fetches the
// one file described by h, whose h.Size bytes are read from body. in and out are
// the session's stdin and stdout. When h carries times they are sent ahead of the
// file, as "scp -p -f" does; leave them zero unless the peer asked for -p.
func Send(in io.Reader, out io.Writer, h Header, body io.Reader) error {
	if err := validName(h.Name); err != nil {
		return err
	}
	r := bufio.NewReader(in)

	// The peer says it is ready.
	if err := readStatus(r); err != nil {
		return err
	}
	if h.hasTimes() {
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
	// It accepts the header before taking the body.
	if err := readStatus(r); err != nil {
		return err
	}

	n, err := io.CopyN(out, body, h.Size)
	if err != nil {
		// The peer is counting bytes: make up the promised size, then say the
		// transfer failed, so it neither hangs nor keeps a short file as whole.
		if _, padErr := io.CopyN(out, zeros{}, h.Size-n); padErr != nil {
			return padErr
		}
		_ = writeFailure(out, "read error: "+err.Error())
		return err
	}
	if err := writeOK(out); err != nil {
		return err
	}

	// And reports whether it stored the body.
	return readStatus(r)
}

// SendFailure answers a fetch that cannot be served, as "scp -f" does for a file it
// cannot open: it waits for the peer to say it is ready, then reports message.
func SendFailure(in io.Reader, out io.Writer, message string) error {
	if err := readStatus(bufio.NewReader(in)); err != nil {
		return err
	}
	return writeFailure(out, message)
}

// zeros reads as an endless run of zero bytes.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
