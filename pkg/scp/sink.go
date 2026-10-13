package scp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// A Sink takes what arrives at the receiving end of a transfer: any number of
// files and, when directories are allowed, directories holding more of them.
type Sink interface {
	// File receives one file. body reads exactly h.Size bytes; whatever File
	// leaves unread is drained afterwards. An error fails this file only: the
	// sender is told, and the transfer carries on with the next entry.
	File(h Header, body io.Reader) error

	// EnterDir starts a directory named h.Name. The entries that follow belong
	// to it until the matching LeaveDir. An error ends the transfer.
	EnterDir(h Header) error

	// LeaveDir ends the directory most recently entered.
	LeaveDir() error
}

// ReceiveOptions shape what ReceiveAll accepts.
type ReceiveOptions struct {
	// MaxSize, when positive, refuses a file that declares more bytes. The sender
	// is told, skips that file, and the transfer carries on.
	MaxSize int64

	// Recursive accepts directories, as "scp -r -t" does. Without it a
	// directory ends the transfer.
	Recursive bool

	// single ends the transfer after its first file, as Receive and Download
	// expect one file and return as soon as it is in.
	single bool
}

// ReceiveAll serves the receiving end of a transfer, as "scp -t" does, for any
// number of files and, with opts.Recursive, directories. It reads until the sender
// closes its side. in and out are the session's stdin and stdout.
//
// A file that fails, whether the sender reports it could not read it or sink
// refuses it, does not end the transfer; ReceiveAll goes on and returns all such
// failures joined. A broken protocol or a failed directory ends it at once.
func ReceiveAll(in io.Reader, out io.Writer, opts ReceiveOptions, sink Sink) error {
	return receiveEntries(bufio.NewReader(in), out, opts, sink, nil)
}

// nameCheck vets a name the sender gives at a directory depth (0 for the top
// level) before anything is written under it.
type nameCheck func(name string, depth int) error

// receiveEntries is the receiving end shared by ReceiveAll, Receive, the client's
// downloads and the copies between hosts.
func receiveEntries(r *bufio.Reader, out io.Writer, opts ReceiveOptions, sink Sink, check nameCheck) error {
	// Ready for the first entry.
	if err := writeOK(out); err != nil {
		return err
	}
	rc := &receiving{r: r, out: out, opts: opts, sink: sink, check: check}
	for {
		line, err := readLine(r)
		if err == io.EOF && line == "" {
			return rc.end()
		}
		if err != nil {
			return fmt.Errorf("%w: reading a control line: %v", ErrProtocol, err)
		}
		if done, err := rc.handle(strings.TrimRight(line, "\n")); done || err != nil {
			return err
		}
	}
}

// receiving is the receiving end of a transfer between control lines.
type receiving struct {
	r     *bufio.Reader
	out   io.Writer
	opts  ReceiveOptions
	sink  Sink
	check nameCheck

	times    Header // from a time line, waiting for the entry it goes with
	depth    int    // directories entered and not yet left
	failures []error
}

// handle acts on one control line, without its newline. done reports that the
// transfer is over, with err as its result.
func (rc *receiving) handle(line string) (done bool, err error) {
	if line == "" {
		return true, rc.refuse("malformed control line", fmt.Errorf("%w: empty control line", ErrProtocol))
	}
	switch line[0] {
	case statusError, statusFatal:
		return rc.senderFailed(line)
	case 'T':
		return false, rc.timeLine(line)
	case 'C':
		return rc.file(line)
	case 'D':
		return false, rc.enterDir(line)
	case 'E':
		return false, rc.leaveDir()
	default:
		return true, rc.refuse("malformed control line", fmt.Errorf("%w: unexpected control line %q", ErrProtocol, line))
	}
}

// refuse tells the sender message and returns err, which ends the transfer.
func (rc *receiving) refuse(message string, err error) error {
	_ = writeFailure(rc.out, message)
	return err
}

// end is the transfer's result once the sender has closed its side.
func (rc *receiving) end() error {
	if rc.depth != 0 {
		return fmt.Errorf("%w: the sender stopped inside a directory", ErrProtocol)
	}
	return errors.Join(rc.failures...)
}

// senderFailed takes the sender's report of a failure of its own, such as a file
// it could not read. The transfer carries on unless the failure was fatal.
func (rc *receiving) senderFailed(line string) (done bool, err error) {
	failure := &RemoteError{Message: line[1:], Fatal: line[0] == statusFatal}
	rc.failures = append(rc.failures, failure)
	if failure.Fatal {
		return true, errors.Join(rc.failures...)
	}
	return false, nil
}

func (rc *receiving) timeLine(line string) error {
	if err := parseTimes(line, &rc.times); err != nil {
		return rc.refuse("malformed time line", err)
	}
	return writeOK(rc.out)
}

// entry reads the header of a file or directory line, with the times that came
// ahead of it, and checks its name. malformed is what the sender is told when the
// line does not parse.
func (rc *receiving) entry(line, malformed string) (Header, error) {
	h, err := parseHeader(line)
	if err != nil {
		return Header{}, rc.refuse(malformed, err)
	}
	h.ModTime, h.AccessTime = rc.times.ModTime, rc.times.AccessTime
	rc.times = Header{}
	if err := checkName(h.Name, rc.depth, rc.check); err != nil {
		return Header{}, rc.refuse(err.Error(), err)
	}
	return h, nil
}

func (rc *receiving) file(line string) (done bool, err error) {
	h, err := rc.entry(line, "malformed control line")
	if err != nil {
		return true, err
	}
	if rc.opts.MaxSize > 0 && h.Size > rc.opts.MaxSize {
		// Refused before the body: the sender skips it and goes on.
		if err := writeFailure(rc.out, h.Name+": file too large"); err != nil {
			return true, err
		}
		rc.failures = append(rc.failures, fmt.Errorf("scp: %q declares %d bytes, more than the %d accepted", h.Name, h.Size, rc.opts.MaxSize))
	} else {
		failure, err := receiveBody(rc.r, rc.out, h, rc.sink)
		if err != nil {
			return true, err
		}
		if failure != nil {
			rc.failures = append(rc.failures, failure)
		}
	}
	if rc.opts.single {
		return true, errors.Join(rc.failures...)
	}
	return false, nil
}

func (rc *receiving) enterDir(line string) error {
	if !rc.opts.Recursive {
		return rc.refuse("received a directory without -r", fmt.Errorf("%w: a directory was sent without -r", ErrProtocol))
	}
	h, err := rc.entry("C"+line[1:], "malformed directory line")
	if err != nil {
		return err
	}
	if err := rc.sink.EnterDir(h); err != nil {
		return rc.refuse(failureMessage(err), err)
	}
	rc.depth++
	return writeOK(rc.out)
}

func (rc *receiving) leaveDir() error {
	if rc.depth == 0 {
		return rc.refuse("end of a directory that was never started", fmt.Errorf("%w: unmatched end of directory", ErrProtocol))
	}
	rc.depth--
	if err := rc.sink.LeaveDir(); err != nil {
		return rc.refuse(failureMessage(err), err)
	}
	return writeOK(rc.out)
}

// receiveBody takes one file's body after its control line. It returns the file's
// own failure, which the transfer survives, separately from an error that ends it.
func receiveBody(r *bufio.Reader, out io.Writer, h Header, sink Sink) (failure, err error) {
	if err := writeOK(out); err != nil {
		return nil, err
	}
	body := io.LimitReader(r, h.Size)
	fileErr := sink.File(h, body)
	if _, err := io.Copy(io.Discard, body); err != nil {
		return nil, fmt.Errorf("%w: reading the body of %q: %w", ErrProtocol, h.Name, err)
	}

	// The sender reports its own view of the file before hearing ours.
	status, err := r.ReadByte()
	if err != nil {
		// What the sink made of the file is still worth knowing.
		return nil, errors.Join(fmt.Errorf("%w: no status byte after %q: %v", ErrProtocol, h.Name, err), fileErr)
	}
	if senderErr := checkStatus(r, status); senderErr != nil && fileErr == nil {
		fileErr = senderErr
	}

	if fileErr != nil {
		if err := writeFailure(out, failureMessage(fileErr)); err != nil {
			return nil, err
		}
		return fileErr, nil
	}
	return nil, writeOK(out)
}

// checkName refuses a name that could reach outside the directory it arrives in,
// then applies the caller's own check, if any.
func checkName(name string, depth int, check nameCheck) error {
	if err := validName(name); err != nil {
		return err
	}
	if check != nil {
		return check(name, depth)
	}
	return nil
}

// oneFile is a Sink for a transfer of a single file, which it hands to take.
// Directories never reach it: without Recursive they end the transfer first.
type oneFile struct {
	Sink
	take func(Header, io.Reader) error
	got  bool
	h    Header
}

func (s *oneFile) File(h Header, body io.Reader) error {
	s.got, s.h = true, h
	return s.take(h, body)
}

// receive takes the one file the sender sends, refusing it when it declares more
// than maxSize bytes, and returns as soon as it is in.
func (s *oneFile) receive(r *bufio.Reader, out io.Writer, maxSize int64) error {
	err := receiveEntries(r, out, ReceiveOptions{MaxSize: maxSize, single: true}, s, nil)
	if err == nil && !s.got {
		err = fmt.Errorf("%w: the sender ended without sending a file", ErrProtocol)
	}
	return err
}
