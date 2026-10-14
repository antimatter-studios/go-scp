package scp

import (
	"bufio"
	"errors"
	"io"
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
	return (&oneFile{take: accept}).receive(bufio.NewReader(in), out, ReceiveOptions{MaxSize: maxSize})
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
	failure, err := sender{r: r, out: out}.file(h, body, h.Name)
	if err != nil {
		return err
	}
	return failure
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
