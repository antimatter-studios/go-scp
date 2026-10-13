package scp

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// DirSinkOptions shape how a DirSink writes.
type DirSinkOptions struct {
	// Preserve applies received modes to existing files and directories, and
	// received times, as "scp -p" does. New entries take their mode either way,
	// less the umask.
	Preserve bool
}

// DirSink is a Sink that writes to the local filesystem under one target, as
// "scp -t target" does. When the target is an existing directory, entries are
// created inside it; otherwise the first entry is created as the target itself.
//
// Nothing is written outside the target's directory: names are checked before they
// arrive, and every write goes through an os.Root, which refuses a path that would
// leave it, through a symlink or otherwise.
type DirSink struct {
	root  *os.Root // the target, or the directory holding it when it is not a directory
	first string   // the target's own name when it is not a directory; "" when it is
	used  bool     // whether the target's own name has been taken
	opts  DirSinkOptions
	dirs  []openDir // the directories entered and not yet left, innermost last
}

// openDir is a directory entered and not yet left. It is held open as a root of its
// own, so its entries are created by name in it rather than by a path from the
// target that grows with every level, and would cost more at every level.
type openDir struct {
	root   *os.Root // the directory itself
	parent *os.Root // the directory holding it
	name   string   // its name in parent
	rel    string   // its path under the target
	h      Header
}

// NewDirSink returns a DirSink writing under target. Close it when done.
//
// target itself is opened as given, following any symlink on the way to it; only
// what is written under it is held there. For a path a client chose, use
// NewDirSinkIn, which finds the target inside a root as well.
func NewDirSink(target string, opts DirSinkOptions) (*DirSink, error) {
	s := &DirSink{opts: opts}
	info, err := os.Stat(target)
	switch {
	case err == nil && info.IsDir():
		s.root, err = os.OpenRoot(target)
	case err == nil || errors.Is(err, fs.ErrNotExist):
		s.first = filepath.Base(target)
		s.root, err = os.OpenRoot(filepath.Dir(target))
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// NewDirSinkIn returns a DirSink writing to rel, a slash-separated path under
// root. Unlike NewDirSink, the target itself is found inside root too, so a symlink
// on the way to it can no more lead out of root than an entry can lead out of the
// target. This is the one a server wants: rel is what a client asked for.
func NewDirSinkIn(root *os.Root, rel string, opts DirSinkOptions) (*DirSink, error) {
	s := &DirSink{opts: opts}
	rel = filepath.FromSlash(rel)
	info, err := root.Stat(rel)
	switch {
	case err == nil && info.IsDir():
		s.root, err = root.OpenRoot(rel)
	case err == nil || errors.Is(err, fs.ErrNotExist):
		s.first = filepath.Base(rel)
		s.root, err = root.OpenRoot(filepath.Dir(rel))
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Close releases the target directory, and any directory entered and not left.
func (s *DirSink) Close() error {
	for _, d := range s.dirs {
		d.root.Close()
	}
	s.dirs = nil
	return s.root.Close()
}

// ownerAll lets a directory's owner list, enter and add to it, which a DirSink
// needs while it writes the directory's entries, whatever mode it ends with.
const ownerAll = 0o700

// maxPath is the longest path a DirSink writes under its target, the PATH_MAX of
// Linux. OpenSSH's scp stops at the system's limit because it builds whole paths;
// an os.Root has no such limit, so without one a sender could nest directories
// without end, each one held open.
const maxPath = 4096

// place is where an entry goes: the root it is created in, its name there, and its
// path under the target.
type place struct {
	root *os.Root
	name string
	rel  string
}

// placeOf returns where an entry named name goes.
func (s *DirSink) placeOf(name string) (place, error) {
	if len(s.dirs) > 0 {
		d := s.dirs[len(s.dirs)-1]
		rel := path.Join(d.rel, name)
		if len(rel) > maxPath {
			return place{}, fmt.Errorf("scp: a path under the target longer than %d bytes", maxPath)
		}
		return place{d.root, name, rel}, nil
	}
	if s.first == "" {
		return place{s.root, name, name}, nil
	}
	if s.used {
		return place{}, fmt.Errorf("scp: %s is not a directory, so it takes only one entry", s.first)
	}
	s.used = true
	return place{s.root, s.first, s.first}, nil
}

// File writes one file the way scp does: in place over an existing file, then cut
// to the new length, so an existing file keeps its owner. It writes at most h.Size
// bytes, and a body that ends sooner fails the file, which keeps only what arrived.
func (s *DirSink) File(h Header, body io.Reader) error {
	if h.Size < 0 {
		return fmt.Errorf("scp: %s: negative size %d", h.Name, h.Size)
	}
	at, err := s.placeOf(h.Name)
	if err != nil {
		return err
	}
	file, err := at.root.OpenFile(at.name, os.O_WRONLY|os.O_CREATE, h.Mode)
	if err != nil {
		return err
	}
	defer file.Close()
	n, err := io.CopyN(file, body, h.Size)
	if err == io.EOF {
		err = fmt.Errorf("scp: %s: only %d of %d bytes arrived", at.rel, n, h.Size)
	}
	// Cut an existing longer file down to what was written, and never pad a short
	// one out to the size it was meant to have.
	if truncErr := file.Truncate(n); err == nil {
		err = truncErr
	}
	if err != nil || !s.opts.Preserve {
		return err
	}
	// Times are set only once the mode is, as scp does.
	err = file.Chmod(h.Mode)
	if err == nil {
		err = applyTimes(at.root, at.name, h)
	}
	return err
}

// EnterDir creates the directory unless it exists, writable by its owner until it
// is left so that its entries can be created, and opens it for them.
func (s *DirSink) EnterDir(h Header) error {
	at, err := s.placeOf(h.Name)
	if err != nil {
		return err
	}
	info, err := at.root.Stat(at.name)
	switch {
	case err == nil && !info.IsDir():
		return fmt.Errorf("scp: %s: not a directory", at.rel)
	case errors.Is(err, fs.ErrNotExist):
		err = at.root.Mkdir(at.name, h.Mode|ownerAll)
	}
	if err != nil {
		return err
	}
	dir, err := at.root.OpenRoot(at.name)
	if err != nil {
		return err
	}
	s.dirs = append(s.dirs, openDir{root: dir, parent: at.root, name: at.name, rel: at.rel, h: h})
	return nil
}

// LeaveDir closes the directory most recently entered, applying its mode and times
// now that its entries are written, when preserving.
func (s *DirSink) LeaveDir() error {
	if len(s.dirs) == 0 {
		return errors.New("scp: no directory to leave")
	}
	d := s.dirs[len(s.dirs)-1]
	s.dirs = s.dirs[:len(s.dirs)-1]
	d.root.Close()
	if !s.opts.Preserve {
		return nil
	}
	if err := d.parent.Chmod(d.name, d.h.Mode); err != nil {
		return err
	}
	return applyTimes(d.parent, d.name, d.h)
}

// applyTimes sets the times h carries on name in root.
func applyTimes(root *os.Root, name string, h Header) error {
	if !h.hasTimes() {
		return nil
	}
	atime, mtime := h.AccessTime, h.ModTime
	if atime.IsZero() {
		atime = mtime
	}
	if mtime.IsZero() {
		mtime = atime
	}
	return root.Chtimes(name, atime, mtime)
}
