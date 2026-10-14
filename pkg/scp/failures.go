package scp

import (
	"errors"

	"golang.org/x/crypto/ssh"
)

// FileError is one file or directory that a copy skipped while it went on with
// the rest. Its message is Err's, and it unwraps to Err.
type FileError struct {
	// Path is the entry's path in the copy, as Progress names it: its top-level
	// name, then the path inside that. It is empty when no name can be given: only
	// the remote's own message names the file, as when the classic protocol's
	// remote scp cannot read a source, or the name the remote gave is not a plain
	// file name.
	Path string

	Err error
}

func (e *FileError) Error() string { return e.Err.Error() }
func (e *FileError) Unwrap() error { return e.Err }

// skipped returns err as the failure of the entry at path. An err that already is
// one is given path instead of its own: the caller, further up, knows more of it.
func skipped(path string, err error) error {
	if f, ok := err.(*FileError); ok {
		err = f.Err
	}
	return &FileError{Path: path, Err: err}
}

// Failures splits a copy's error into the files and directories it skipped, each a
// *FileError, and what is left: nil when the copy went on to the end and those were
// its only failures, and otherwise what stopped it. CopyOnHost cannot tell: the
// copy is the remote scp's, and its error is that program's own, all of it rest.
func Failures(err error) (files []*FileError, rest error) {
	rest = split(err, &files)
	return files, rest
}

// split adds the FileErrors in err to files, looking inside errors joined with
// errors.Join, and returns what else err holds.
func split(err error, files *[]*FileError) error {
	switch e := err.(type) {
	case *FileError:
		*files = append(*files, e)
		return nil
	case interface{ Unwrap() []error }:
		var rest []error
		for _, inner := range e.Unwrap() {
			if r := split(inner, files); r != nil {
				rest = append(rest, r)
			}
		}
		return errors.Join(rest...)
	default:
		return err
	}
}

// joinFailures puts a copy's skipped files and the rest of its error back into one
// error, as Failures splits it.
func joinFailures(files []*FileError, rest error) error {
	all := make([]error, 0, len(files)+1)
	for _, f := range files {
		all = append(all, f)
	}
	return errors.Join(append(all, rest)...)
}

// failedFiles reports whether err is what an scp program exits with when some of
// the files it copied failed and nothing else did: exit status 1.
func failedFiles(err error) bool {
	var exit *ssh.ExitError
	return err == nil || errors.As(err, &exit) && exit.ExitStatus() == 1
}
