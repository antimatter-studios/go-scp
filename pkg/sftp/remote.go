package sftp

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

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
}

// writeFile writes h.Size bytes of body to path, in place over an existing file,
// which keeps its own mode unless preserving. A new file takes h.Mode, less 022 as
// a server would take its umask off, before any of it is written: the server
// creates it with a mode of its own choosing, which may let others read it. With
// preserve the file takes h's mode and times exactly. A body that ends sooner fails
// the file.
func (w remoteWriter) writeFile(path string, h scp.Header, body io.Reader) error {
	_, statErr := w.conn.Stat(path)
	created := errors.Is(statErr, fs.ErrNotExist)
	out, err := w.conn.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("sftp: %s: %w", path, err)
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
		n, err = io.CopyN(struct{ io.Writer }{out}, body, h.Size)
		if err == io.EOF {
			err = fmt.Errorf("only %d of %d bytes arrived", n, h.Size)
		}
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil && w.preserve {
		err = w.conn.Chtimes(path, h.AccessTime, h.ModTime)
	}
	if err != nil {
		return fmt.Errorf("sftp: %s: %w", path, err)
	}
	return nil
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
