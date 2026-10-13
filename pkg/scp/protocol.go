package scp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Status bytes. Every step of a transfer is answered with one of these, and the two
// failures are followed by a message ending in a newline.
const (
	statusOK    byte = 0
	statusError byte = 1 // this transfer failed; the program carries on
	statusFatal byte = 2 // the program gives up
)

// ErrProtocol reports a peer that did not follow the protocol.
var ErrProtocol = errors.New("scp: protocol violation")

// RemoteError is a failure the other end reported, in its own words.
type RemoteError struct {
	// Message is what the other end said, without its trailing newline.
	Message string
	// Fatal is set when the other end gave up altogether rather than failing one
	// transfer.
	Fatal bool
}

func (e *RemoteError) Error() string { return e.Message }

// Header is the control line that announces a file: "C<mode> <size> <name>".
type Header struct {
	// Mode holds the file's permission bits.
	Mode os.FileMode
	// Size is the number of bytes that follow the control line.
	Size int64
	// Name is the file's name, without a directory. A received name comes from the
	// peer: treat it as untrusted, never as a path.
	Name string
}

// String returns h as a control line, without its newline.
func (h Header) String() string {
	return fmt.Sprintf("C%04o %d %s", h.Mode.Perm(), h.Size, h.Name)
}

// parseHeader reads a control line, without its newline. The name may contain
// spaces, so only the first two fields are delimited.
func parseHeader(line string) (Header, error) {
	if !strings.HasPrefix(line, "C") {
		return Header{}, fmt.Errorf("%w: %q is not a file control line", ErrProtocol, line)
	}
	parts := strings.SplitN(line[1:], " ", 3)
	if len(parts) != 3 || parts[2] == "" {
		return Header{}, fmt.Errorf("%w: malformed control line %q", ErrProtocol, line)
	}
	mode, err := strconv.ParseUint(parts[0], 8, 32)
	if err != nil || mode > 0o7777 {
		return Header{}, fmt.Errorf("%w: bad mode in %q", ErrProtocol, line)
	}
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || size < 0 {
		return Header{}, fmt.Errorf("%w: bad size in %q", ErrProtocol, line)
	}
	return Header{Mode: os.FileMode(mode).Perm(), Size: size, Name: parts[2]}, nil
}

// validName refuses a name the receiving end would refuse, or would read as more
// than one file name: a directory part, "." or "..", or a line break.
func validName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\n") {
		return fmt.Errorf("scp: %q cannot be sent as a file name", name)
	}
	return nil
}

func writeOK(w io.Writer) error {
	_, err := w.Write([]byte{statusOK})
	return err
}

// writeFailure reports a failed transfer. The message is advisory, the peer prints
// it, but the status byte is what makes the transfer fail.
func writeFailure(w io.Writer, message string) error {
	message = strings.ReplaceAll(message, "\n", " ")
	_, err := w.Write(append([]byte{statusError}, message+"\n"...))
	return err
}

// readStatus reads one status byte. A reported failure becomes a *RemoteError
// carrying the message that follows it.
func readStatus(r *bufio.Reader) error {
	status, err := r.ReadByte()
	if err != nil {
		return fmt.Errorf("%w: expected a status byte: %v", ErrProtocol, err)
	}
	return checkStatus(r, status)
}

// checkStatus turns a status byte already read into nil, a *RemoteError with the
// message that follows a failure, or ErrProtocol for anything else.
func checkStatus(r *bufio.Reader, status byte) error {
	switch status {
	case statusOK:
		return nil
	case statusError, statusFatal:
		return &RemoteError{Message: readMessage(r), Fatal: status == statusFatal}
	default:
		return fmt.Errorf("%w: unexpected status byte %#x", ErrProtocol, status)
	}
}

// readMessage reads the message line after a failure status. A peer that hangs up
// before finishing it still failed, so whatever arrived is the message.
func readMessage(r *bufio.Reader) string {
	line, _ := r.ReadString('\n')
	return strings.TrimRight(line, "\n")
}

// Quote quotes s as a single word for a POSIX shell. An SSH server hands a command
// to the login user's shell, so a path in one must be quoted to arrive intact.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
