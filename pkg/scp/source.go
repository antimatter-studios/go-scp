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

	// Progress, when set, is told how each file's body moves.
	Progress Progress
}

// SendFS serves the sending end of a transfer, as "scp -f" does, for the named
// entries of fsys, in order: each a file or, with opts.Recursive, a directory sent
// with its contents. in and out are the session's stdin and stdout.
//
// An entry that cannot be sent, because it is missing, unreadable or refused by
// the peer, is reported to the peer and skipped; SendFS goes on and returns all
// such failures joined, each a FileError that Failures finds. A broken protocol
// ends the transfer at once.
func SendFS(in io.Reader, out io.Writer, fsys fs.FS, names []string, opts SendOptions) error {
	sources := make([]source, len(names))
	for i, name := range names {
		sources[i] = source{fsys: fsys, name: name}
	}
	return newSending(bufio.NewReader(in), out, opts).entries(sources)
}

// source is one top-level entry to send: a name within a filesystem.
type source struct {
	fsys fs.FS
	name string
}

// sending is the sending end of a transfer of files and directories: the sender
// that speaks to the receiver, and the options that shape what it sends.
type sending struct {
	sender
	opts SendOptions
}

func newSending(r *bufio.Reader, out io.Writer, opts SendOptions) sending {
	return sending{sender{r: r, out: out, progress: opts.Progress}, opts}
}

// entries sends each source in turn: the sending end shared by SendFS and the
// client's uploads.
func (s sending) entries(sources []source) error {
	// The receiver says it is ready.
	if err := readStatus(s.r); err != nil {
		return err
	}
	var failures []error
	for _, src := range sources {
		failure, err := s.entry(src.fsys, src.name)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		if failure != nil {
			failures = append(failures, failure)
		}
	}
	return errors.Join(failures...)
}

// entry sends one file or directory. It returns the entry's own failure, which
// the transfer survives, separately from an error that ends it.
func (s sending) entry(fsys fs.FS, name string) (failure, err error) {
	info, statErr := fs.Stat(fsys, name)
	if statErr != nil {
		return report(s.out, name, statErr)
	}
	h := Header{Mode: info.Mode().Perm(), Size: info.Size(), Name: path.Base(name)}
	if s.opts.Preserve {
		h.ModTime, h.AccessTime = info.ModTime(), AccessTime(info)
	}
	if err := validName(h.Name); err != nil {
		return report(s.out, name, err)
	}

	switch {
	case info.IsDir() && s.opts.Recursive:
		return s.dir(fsys, name, h)
	case info.Mode().IsRegular():
		return s.fromFS(fsys, name, h)
	default:
		return report(s.out, name, errNotRegular)
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
	return skipped(name, fmt.Errorf("scp: %s: %w", name, cause)), nil
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

// entryAnswer sorts the receiver's answer to the entry name: a refusal of just that
// entry is the entry's failure, which the transfer survives; anything else ends it.
func entryAnswer(name string, err error) (failure, fatal error) {
	if receiverRefused(err) {
		return skipped(name, err), nil
	}
	return nil, err
}

// fromFS sends the regular file name in fsys.
func (s sending) fromFS(fsys fs.FS, name string, h Header) (failure, err error) {
	file, openErr := fsys.Open(name)
	if openErr != nil {
		return report(s.out, name, openErr)
	}
	defer file.Close()
	return s.file(h, file, name)
}

// sender speaks the sending side of a transfer to a receiver: r reads its answers,
// out takes what is sent, and progress, when set, is told how each body moves.
type sender struct {
	r        *bufio.Reader
	out      io.Writer
	progress Progress
}

// file sends one file: its time line when h carries times, its control line, and
// h.Size bytes of body, waiting for the receiver's answer after each. When the body
// ends early the promised size is made up with zeros and the file is reported
// failed, so the receiver neither hangs nor keeps a short file as whole; label
// names the file in that report and to progress. It returns the file's own
// failure, which the transfer survives, separately from an error that ends it.
func (s sender) file(h Header, body io.Reader, label string) (failure, err error) {
	body = s.progress.Track(label, 0, h.Size, body)
	if err := sendTimes(s.r, s.out, h); err != nil {
		return entryAnswer(label, err)
	}
	if _, err := io.WriteString(s.out, h.String()+"\n"); err != nil {
		return nil, err
	}
	// The receiver accepts the header, or turns the file down and expects no body.
	if err := readStatus(s.r); err != nil {
		return entryAnswer(label, err)
	}

	n, copyErr := io.CopyN(s.out, body, h.Size)
	if copyErr != nil {
		if _, err := io.CopyN(s.out, zeros{}, h.Size-n); err != nil {
			return nil, err
		}
		failure = fmt.Errorf("scp: %s: %w", label, copyErr)
		if err := writeFailure(s.out, failure.Error()); err != nil {
			return nil, err
		}
	} else if err := writeOK(s.out); err != nil {
		return nil, err
	}

	// The receiver reports whether it stored the file.
	if err := readStatus(s.r); err != nil {
		if !receiverRefused(err) {
			return nil, err
		}
		if failure == nil {
			failure = err
		}
	}
	if failure != nil {
		return skipped(label, failure), nil
	}
	return nil, nil
}

// dir sends the directory name in fsys with everything in it.
func (s sending) dir(fsys fs.FS, name string, h Header) (failure, err error) {
	entries, readErr := fs.ReadDir(fsys, name)
	if readErr != nil {
		return report(s.out, name, readErr)
	}

	if err := sendTimes(s.r, s.out, h); err != nil {
		return entryAnswer(name, err)
	}
	if _, err := fmt.Fprintf(s.out, "D%04o 0 %s\n", h.Mode.Perm(), h.Name); err != nil {
		return nil, err
	}
	if err := readStatus(s.r); err != nil {
		return entryAnswer(name, err)
	}

	var failures []error
	for _, e := range entries {
		entryFailure, err := s.entry(fsys, path.Join(name, e.Name()))
		if err != nil {
			return nil, err
		}
		if entryFailure != nil {
			failures = append(failures, entryFailure)
		}
	}

	if _, err := io.WriteString(s.out, "E\n"); err != nil {
		return nil, err
	}
	if err := readStatus(s.r); err != nil {
		return nil, err
	}
	return errors.Join(failures...), nil
}
