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

// receiveEntries is the receiving end shared by ReceiveAll and the client's
// downloads.
func receiveEntries(r *bufio.Reader, out io.Writer, opts ReceiveOptions, sink Sink, check nameCheck) error {
	// Ready for the first entry.
	if err := writeOK(out); err != nil {
		return err
	}

	var failures []error
	var times Header
	depth := 0
	for {
		line, err := readLine(r)
		if err != nil {
			if err == io.EOF && line == "" {
				if depth != 0 {
					return fmt.Errorf("%w: the sender stopped inside a directory", ErrProtocol)
				}
				return errors.Join(failures...)
			}
			return fmt.Errorf("%w: reading a control line: %v", ErrProtocol, err)
		}
		line = strings.TrimRight(line, "\n")
		if line == "" {
			_ = writeFailure(out, "malformed control line")
			return fmt.Errorf("%w: empty control line", ErrProtocol)
		}

		switch line[0] {
		case statusError, statusFatal:
			// The sender reports a failure of its own, such as a file it could
			// not read, and carries on unless it was fatal.
			failure := &RemoteError{Message: line[1:], Fatal: line[0] == statusFatal}
			if failure.Fatal {
				return errors.Join(append(failures, failure)...)
			}
			failures = append(failures, failure)

		case 'T':
			if err := parseTimes(line, &times); err != nil {
				_ = writeFailure(out, "malformed time line")
				return err
			}
			if err := writeOK(out); err != nil {
				return err
			}

		case 'C':
			h, err := parseHeader(line)
			if err != nil {
				_ = writeFailure(out, "malformed control line")
				return err
			}
			h.ModTime, h.AccessTime = times.ModTime, times.AccessTime
			times = Header{}
			if err := checkName(h.Name, depth, check); err != nil {
				_ = writeFailure(out, err.Error())
				return err
			}
			if opts.MaxSize > 0 && h.Size > opts.MaxSize {
				// Refused before the body: the sender skips it and goes on.
				if err := writeFailure(out, h.Name+": file too large"); err != nil {
					return err
				}
				failures = append(failures, fmt.Errorf("scp: %q declares %d bytes, more than the %d accepted", h.Name, h.Size, opts.MaxSize))
			} else {
				failure, err := receiveBody(r, out, h, sink)
				if err != nil {
					return err
				}
				if failure != nil {
					failures = append(failures, failure)
				}
			}
			if opts.single {
				return errors.Join(failures...)
			}

		case 'D':
			if !opts.Recursive {
				_ = writeFailure(out, "received a directory without -r")
				return fmt.Errorf("%w: a directory was sent without -r", ErrProtocol)
			}
			h, err := parseHeader("C" + line[1:])
			if err != nil {
				_ = writeFailure(out, "malformed directory line")
				return err
			}
			h.ModTime, h.AccessTime = times.ModTime, times.AccessTime
			times = Header{}
			if err := checkName(h.Name, depth, check); err != nil {
				_ = writeFailure(out, err.Error())
				return err
			}
			if err := sink.EnterDir(h); err != nil {
				_ = writeFailure(out, failureMessage(err))
				return err
			}
			depth++
			if err := writeOK(out); err != nil {
				return err
			}

		case 'E':
			if depth == 0 {
				_ = writeFailure(out, "end of a directory that was never started")
				return fmt.Errorf("%w: unmatched end of directory", ErrProtocol)
			}
			depth--
			if err := sink.LeaveDir(); err != nil {
				_ = writeFailure(out, failureMessage(err))
				return err
			}
			if err := writeOK(out); err != nil {
				return err
			}

		default:
			_ = writeFailure(out, "malformed control line")
			return fmt.Errorf("%w: unexpected control line %q", ErrProtocol, line)
		}
	}
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
