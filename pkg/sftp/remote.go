package sftp

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"

	"github.com/antimatter-studios/go-scp/pkg/scp"
	pkgsftp "github.com/pkg/sftp"
)

// defaultUmask is what a server usually takes off the mode of a file it creates,
// and what is taken off a new entry's mode here, since github.com/pkg/sftp cannot
// pass a mode when it creates one.
const defaultUmask = 0o022

// remoteWriter writes files and directories on a remote host over one SFTP
// session: the receiving end that CopyTo and CopyBetween share.
type remoteWriter struct {
	conn     *pkgsftp.Client
	preserve bool
	resume   bool
	atomic   bool
}

// writeFile writes h.Size bytes of body to target, in place over an existing file,
// which keeps its own mode unless preserving. A new file takes h.Mode, less 022 as
// a server would take its umask off, before any of it is written: the server
// creates it with a mode of its own choosing, which may let others read it. With
// preserve the file takes h's mode and times exactly. A body that ends sooner fails
// the file.
//
// From offset on, as resumeFrom gives it, the file is continued rather than
// written over: body holds the rest of the file, from offset on.
//
// With atomic the file is written beside target, as a new file, and renamed over
// it once whole with the posix-rename@openssh.com extension, which replaces target
// in one step; a server without it fails the file. A file that fails is removed,
// unless it is kept for resume to continue.
func (w remoteWriter) writeFile(target string, h scp.Header, body io.Reader, offset int64) error {
	at := target
	if w.atomic {
		at = temporary(target, w.resume)
	}
	err := w.write(at, h, body, offset)
	if err == nil && w.atomic {
		err = w.conn.PosixRename(at, target)
	}
	if err != nil && w.atomic && !w.resume {
		w.conn.Remove(at)
	}
	if err != nil {
		return fmt.Errorf("sftp: %s: %w", target, err)
	}
	return nil
}

// write writes the file h describes at at; see writeFile.
func (w remoteWriter) write(at string, h scp.Header, body io.Reader, offset int64) error {
	_, statErr := w.conn.Stat(at)
	created := errors.Is(statErr, fs.ErrNotExist)
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if offset > 0 {
		flags = os.O_WRONLY
	}
	out, err := w.conn.OpenFile(at, flags)
	if err != nil {
		return err
	}
	switch {
	case w.preserve:
		err = out.Chmod(h.Mode)
	case created:
		err = out.Chmod(h.Mode &^ defaultUmask)
	}
	if err == nil {
		// Copied through plain Write calls: github.com/pkg/sftp's File.ReadFrom
		// drops the error of a write that ends the file part-way through a packet,
		// and reports the copy whole.
		var n int64
		if _, err = out.Seek(offset, io.SeekStart); err == nil {
			n, err = io.CopyN(struct{ io.Writer }{out}, body, h.Size-offset)
		}
		if err == io.EOF {
			err = fmt.Errorf("only %d of %d bytes arrived", offset+n, h.Size)
		}
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil && w.preserve {
		err = w.conn.Chtimes(at, h.AccessTime, h.ModTime)
	}
	return err
}

// temporary is where an atomic copy writes the file for target until it is whole:
// beside it, under a name starting with a dot. For resume the name is fixed, so a
// later copy finds the part written; otherwise it is random, so copies to the same
// name at once do not write into one file.
func temporary(target string, resume bool) string {
	dir, name := path.Split(target)
	if resume {
		return dir + "." + name + ".partial"
	}
	var random [6]byte
	rand.Read(random[:]) // never fails
	return dir + "." + name + "." + hex.EncodeToString(random[:]) + ".partial"
}

// resumeFrom is where a copy of the file h describes continues at target: the size
// of the file there when resuming, and 0 when not resuming or when there is none;
// with atomic, the part a copy that failed kept. A file there that is longer than
// h.Size is not resumed, and is an error.
func (w remoteWriter) resumeFrom(target string, h scp.Header) (int64, error) {
	if !w.resume {
		return 0, nil
	}
	at := target
	if w.atomic {
		at = temporary(target, true)
	}
	info, err := w.conn.Stat(at)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("sftp: %s: %w", target, err)
	case info.Size() > h.Size:
		return 0, fmt.Errorf("sftp: %s: %d bytes there, more than the %d being copied; not resuming", target, info.Size(), h.Size)
	default:
		return info.Size(), nil
	}
}

// openDir makes the directory at path unless it exists, and reports whether it
// made it.
func (w remoteWriter) openDir(path string) (created bool, err error) {
	existing, err := w.conn.Stat(path)
	created = errors.Is(err, fs.ErrNotExist)
	switch {
	case err == nil && !existing.IsDir():
		return false, fmt.Errorf("sftp: %s: not a directory", path)
	case created:
		err = w.conn.Mkdir(path)
	}
	if err != nil {
		return false, fmt.Errorf("sftp: %s: %w", path, err)
	}
	return created, nil
}

// closeDir gives a directory h's mode, and with preserve its times, once its
// entries are in, so a directory without write permission still takes them. A
// directory that already existed keeps its mode unless preserving.
func (w remoteWriter) closeDir(path string, h scp.Header, created bool) error {
	var err error
	switch {
	case w.preserve:
		err = w.conn.Chmod(path, h.Mode)
		if err == nil {
			err = w.conn.Chtimes(path, h.AccessTime, h.ModTime)
		}
	case created:
		err = w.conn.Chmod(path, h.Mode&^defaultUmask)
	}
	if err != nil {
		return fmt.Errorf("sftp: %s: %w", path, err)
	}
	return nil
}
