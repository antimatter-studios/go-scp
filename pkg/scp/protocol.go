package scp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Status bytes. Every step of a transfer is answered with one of these, and the two
// failures are followed by a message ending in a newline.
const (
	statusOK    byte = 0
	statusError byte = 1 // this transfer failed; the program carries on
	statusFatal byte = 2 // the program gives up
)

// Limits on what a control line may carry.
const (
	// maxMode is the most a mode may be: the permission bits, with setuid, setgid
	// and the sticky bit.
	maxMode = 0o7777
	// maxMicroseconds is the most a time line's microseconds may be, below one
	// whole second.
	maxMicroseconds = 999_999
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
	// ModTime and AccessTime are the file's times. They travel in a time line
	// ("T<mtime> <usec> <atime> <usec>") ahead of the control line, which the sender
	// writes only when times are preserved ("scp -p"); zero when none was sent.
	ModTime    time.Time
	AccessTime time.Time
}

// hasTimes reports whether h carries times to send.
func (h Header) hasTimes() bool {
	return !h.ModTime.IsZero() || !h.AccessTime.IsZero()
}

// timesLine returns h's time line, without its newline. A missing time is sent as
// the other one, so the receiver never sets a file to the epoch.
func (h Header) timesLine() string {
	mtime, atime := h.ModTime, h.AccessTime
	if mtime.IsZero() {
		mtime = atime
	}
	if atime.IsZero() {
		atime = mtime
	}
	return fmt.Sprintf("T%d %d %d %d", mtime.Unix(), microseconds(mtime), atime.Unix(), microseconds(atime))
}

// microseconds is the part of t below a whole second, in microseconds, as a time
// line carries it.
func microseconds(t time.Time) int {
	return t.Nanosecond() / int(time.Microsecond)
}

// parseTimes reads a time line, without its newline, into h's times.
func parseTimes(line string, h *Header) error {
	if !strings.HasPrefix(line, "T") {
		return fmt.Errorf("%w: %q is not a time line", ErrProtocol, line)
	}
	fields := strings.Split(line[1:], " ")
	if len(fields) != 4 {
		return fmt.Errorf("%w: malformed time line %q", ErrProtocol, line)
	}
	var n [4]int64
	for i, f := range fields {
		v, err := strconv.ParseInt(f, 10, 64)
		if err != nil || v < 0 || (i%2 == 1 && v > maxMicroseconds) {
			return fmt.Errorf("%w: malformed time line %q", ErrProtocol, line)
		}
		n[i] = v
	}
	h.ModTime = time.Unix(n[0], n[1]*int64(time.Microsecond))
	h.AccessTime = time.Unix(n[2], n[3]*int64(time.Microsecond))
	return nil
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
	if err != nil || mode > maxMode {
		return Header{}, fmt.Errorf("%w: bad mode in %q", ErrProtocol, line)
	}
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || size < 0 {
		return Header{}, fmt.Errorf("%w: bad size in %q", ErrProtocol, line)
	}
	return Header{Mode: os.FileMode(mode).Perm(), Size: size, Name: parts[2]}, nil
}

// validName refuses a name the receiving end would refuse, or would read as more
// than one file name: a directory part, "." or "..", or a line break. On Windows a
// backslash or a colon is a directory part or a drive too.
func validName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\n") || (runtime.GOOS == "windows" && strings.ContainsAny(name, `\:`)) {
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

// maxLine is the longest protocol line read, newline included: a control line, or
// the message after a failure status. OpenSSH's scp reads these into a fixed buffer
// of a few kilobytes as well. Without a limit, a peer that never sent a newline
// would make the reader's buffer grow until memory ran out.
const maxLine = 4096

// errLineTooLong is a line that reached maxLine without ending.
var errLineTooLong = fmt.Errorf("a line longer than %d bytes", maxLine)

// readLine reads up to and including the next newline, as ReadString('\n') does,
// but gives up with errLineTooLong once the line passes maxLine bytes.
func readLine(r *bufio.Reader) (string, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > maxLine {
			return "", errLineTooLong
		}
		if err != bufio.ErrBufferFull {
			return string(line), err
		}
	}
}

// readMessage reads the message line after a failure status. A peer that hangs up
// before finishing it still failed, so whatever arrived is the message. A message
// longer than maxLine is cut there, and the rest of its line is read past so the
// stream stays in step.
func readMessage(r *bufio.Reader) string {
	var msg []byte
	for {
		part, err := r.ReadSlice('\n')
		msg = append(msg, part[:min(len(part), maxLine-len(msg))]...)
		if err != bufio.ErrBufferFull {
			return strings.TrimRight(string(msg), "\n")
		}
	}
}

// Quote quotes s as a single word for a POSIX shell. An SSH server hands a command
// to the login user's shell, so a path in one must be quoted to arrive intact.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
