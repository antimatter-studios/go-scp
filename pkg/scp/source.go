package scp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
)

// SendOptions shape what SendFS sends.
type SendOptions struct {
	// Recursive sends a directory with everything in it, as "scp -r -f" does.
	// Without it a directory is reported to the peer as not a regular file.
	Recursive bool

	// Preserve sends each entry's modification time ahead of it, as "scp -p -f"
	// does. Set it only when the peer asked for -p.
	Preserve bool
}

// SendFS serves the sending end of a transfer, as "scp -f" does, for the named
// entries of fsys, in order: each a file or, with opts.Recursive, a directory sent
// with its contents. in and out are the session's stdin and stdout.
//
// An entry that cannot be sent, because it is missing, unreadable or refused by
// the peer, is reported to the peer and skipped; SendFS goes on and returns all
// such failures joined. A broken protocol ends the transfer at once.
func SendFS(in io.Reader, out io.Writer, fsys fs.FS, names []string, opts SendOptions) error {
	sources := make([]source, len(names))
	for i, name := range names {
		sources[i] = source{fsys: fsys, name: name}
	}
	return sendEntries(bufio.NewReader(in), out, sources, opts)
}

// source is one top-level entry to send: a name within a filesystem.
type source struct {
	fsys fs.FS
	name string
}

// sendEntries is the sending end shared by SendFS and the client's uploads.
func sendEntries(r *bufio.Reader, out io.Writer, sources []source, opts SendOptions) error {
	// The receiver says it is ready.
	if err := readStatus(r); err != nil {
		return err
	}
	var failures []error
	for _, s := range sources {
		failure, err := sendEntry(r, out, s.fsys, s.name, opts)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		if failure != nil {
			failures = append(failures, failure)
		}
	}
	return errors.Join(failures...)
}

// sendEntry sends one file or directory. It returns the entry's own failure, which
// the transfer survives, separately from an error that ends it.
func sendEntry(r *bufio.Reader, out io.Writer, fsys fs.FS, name string, opts SendOptions) (failure, err error) {
	info, statErr := fs.Stat(fsys, name)
	if statErr != nil {
		return report(out, name, statErr)
	}
	h := Header{Mode: info.Mode().Perm(), Size: info.Size(), Name: path.Base(name)}
	if opts.Preserve {
		h.ModTime, h.AccessTime = info.ModTime(), info.ModTime()
	}
	if err := validName(h.Name); err != nil {
		return report(out, name, err)
	}

	switch {
	case info.IsDir() && opts.Recursive:
		return sendDir(r, out, fsys, name, h, opts)
	case info.Mode().IsRegular():
		return sendFile(r, out, fsys, name, h)
	default:
		return report(out, name, errNotRegular)
	}
}

// errNotRegular is why an entry that is neither a regular file nor, when recursive,
// a directory is not sent.
var errNotRegular = errors.New("not a regular file")

// report tells the receiver an entry could not be sent, worded as OpenSSH's scp
// words it, and returns that as the entry's failure.
func report(out io.Writer, name string, cause error) (failure, err error) {
	if err := writeFailure(out, "scp: "+name+": "+describe(cause)); err != nil {
		return nil, err
	}
	return fmt.Errorf("scp: %s: %w", name, cause), nil
}

// describe words an error the way the C library does, which is what scp prints and
// what a peer's user will recognise.
func describe(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "No such file or directory"
	case errors.Is(err, fs.ErrPermission):
		return "Permission denied"
	default:
		return err.Error()
	}
}

// sendTimes sends h's time line when it carries times, and waits for it to be
// accepted.
func sendTimes(r *bufio.Reader, out io.Writer, h Header) error {
	if !h.hasTimes() {
		return nil
	}
	if _, err := io.WriteString(out, h.timesLine()+"\n"); err != nil {
		return err
	}
	return readStatus(r)
}

// receiverRefused reports whether err is the receiver turning down one entry,
// which skips it, rather than an error that ends the transfer.
func receiverRefused(err error) bool {
	var remote *RemoteError
	return errors.As(err, &remote) && !remote.Fatal
}

func sendFile(r *bufio.Reader, out io.Writer, fsys fs.FS, name string, h Header) (failure, err error) {
	file, openErr := fsys.Open(name)
	if openErr != nil {
		return report(out, name, openErr)
	}
	defer file.Close()

	if err := sendTimes(r, out, h); err != nil {
		if receiverRefused(err) {
			return err, nil
		}
		return nil, err
	}
	if _, err := io.WriteString(out, h.String()+"\n"); err != nil {
		return nil, err
	}
	// The receiver accepts the header, or turns the file down and expects no body.
	if err := readStatus(r); err != nil {
		if receiverRefused(err) {
			return err, nil
		}
		return nil, err
	}

	n, copyErr := io.CopyN(out, file, h.Size)
	if copyErr != nil {
		// The receiver is counting bytes: make up the promised size, then say the
		// file failed, so it neither hangs nor keeps a short file as whole.
		if _, err := io.CopyN(out, zeros{}, h.Size-n); err != nil {
			return nil, err
		}
		failure = fmt.Errorf("scp: %s: %w", name, copyErr)
		if err := writeFailure(out, failure.Error()); err != nil {
			return nil, err
		}
	} else if err := writeOK(out); err != nil {
		return nil, err
	}

	// The receiver reports whether it stored the file.
	if err := readStatus(r); err != nil {
		if !receiverRefused(err) {
			return nil, err
		}
		if failure == nil {
			failure = err
		}
	}
	return failure, nil
}

func sendDir(r *bufio.Reader, out io.Writer, fsys fs.FS, name string, h Header, opts SendOptions) (failure, err error) {
	entries, readErr := fs.ReadDir(fsys, name)
	if readErr != nil {
		return report(out, name, readErr)
	}

	if err := sendTimes(r, out, h); err != nil {
		if receiverRefused(err) {
			return err, nil
		}
		return nil, err
	}
	if _, err := fmt.Fprintf(out, "D%04o 0 %s\n", h.Mode.Perm(), h.Name); err != nil {
		return nil, err
	}
	if err := readStatus(r); err != nil {
		if receiverRefused(err) {
			return err, nil
		}
		return nil, err
	}

	var failures []error
	for _, e := range entries {
		entryFailure, err := sendEntry(r, out, fsys, path.Join(name, e.Name()), opts)
		if err != nil {
			return nil, err
		}
		if entryFailure != nil {
			failures = append(failures, entryFailure)
		}
	}

	if _, err := io.WriteString(out, "E\n"); err != nil {
		return nil, err
	}
	if err := readStatus(r); err != nil {
		return nil, err
	}
	return errors.Join(failures...), nil
}
