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
	root  *os.Root
	first string // the target's own name when it is not a directory; "" when it is
	used  bool   // whether the target's own name has been taken
	opts  DirSinkOptions
	dirs  []openDir
}

type openDir struct {
	rel string
	h   Header
}

// NewDirSink returns a DirSink writing under target. Close it when done.
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

// Close releases the target directory.
func (s *DirSink) Close() error { return s.root.Close() }

// rel returns where an entry named name goes, relative to the root.
func (s *DirSink) rel(name string) (string, error) {
	if len(s.dirs) > 0 {
		return path.Join(s.dirs[len(s.dirs)-1].rel, name), nil
	}
	if s.first == "" {
		return name, nil
	}
	if s.used {
		return "", fmt.Errorf("scp: %s is not a directory, so it takes only one entry", s.first)
	}
	s.used = true
	return s.first, nil
}

// File writes one file the way scp does: in place over an existing file, then cut
// to the new length, so an existing file keeps its owner.
func (s *DirSink) File(h Header, body io.Reader) error {
	rel, err := s.rel(h.Name)
	if err != nil {
		return err
	}
	file, err := s.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE, h.Mode)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(file, body); err != nil {
		return err
	}
	if err := file.Truncate(h.Size); err != nil {
		return err
	}
	if !s.opts.Preserve {
		return nil
	}
	// Times are set only once the mode is, as scp does.
	err = file.Chmod(h.Mode)
	if err == nil {
		err = s.applyTimes(rel, h)
	}
	return err
}

// EnterDir creates the directory unless it exists, writable by its owner until it
// is left so that its entries can be created.
func (s *DirSink) EnterDir(h Header) error {
	rel, err := s.rel(h.Name)
	if err != nil {
		return err
	}
	info, err := s.root.Stat(rel)
	switch {
	case err == nil && !info.IsDir():
		return fmt.Errorf("scp: %s: not a directory", rel)
	case errors.Is(err, fs.ErrNotExist):
		if err := s.root.Mkdir(rel, h.Mode|0o700); err != nil {
			return err
		}
	case err != nil:
		return err
	}
	s.dirs = append(s.dirs, openDir{rel: rel, h: h})
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
	if !s.opts.Preserve {
		return nil
	}
	if err := s.root.Chmod(d.rel, d.h.Mode); err != nil {
		return err
	}
	return s.applyTimes(d.rel, d.h)
}

func (s *DirSink) applyTimes(rel string, h Header) error {
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
	return s.root.Chtimes(rel, atime, mtime)
}
