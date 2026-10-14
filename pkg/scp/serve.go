package scp

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Server answers scp commands for an SSH server that handles transfers itself,
// instead of running the scp program, and says what a client may do: read, write
// or both, and how large a file it may send.
type Server struct {
	// Root is where the server reads and writes. Every path is found inside it,
	// symlinks included, so nothing outside it is read or written; a path that
	// climbs out with .. is taken from Root's top.
	Root *os.Root

	// ReadOnly refuses uploads, "scp -t": the client may only fetch files.
	ReadOnly bool

	// WriteOnly refuses downloads, "scp -f": the client may only send files.
	WriteOnly bool

	// MaxSize, when positive, refuses an uploaded file that declares more bytes.
	// The client is told, skips that file and carries on.
	MaxSize int64

	// Atomic writes each uploaded file as DirSinkOptions.Atomic describes, so
	// nothing reading Root sees part of one.
	Atomic bool
}

// The reasons a Server turns a whole command down, which the client is told.
var (
	errReadOnly  = errors.New("scp: this server does not take uploads")
	errWriteOnly = errors.New("scp: this server does not give downloads")
)

// Serve answers one scp command with the root as its only setting; see
// Server.Serve.
func Serve(command string, channel io.ReadWriter, stderr io.Writer, root *os.Root) int {
	return (&Server{Root: root}).Serve(command, channel, stderr)
}

// Serve answers one scp command: command is what the session asked to run, channel
// is the session's stdin and stdout, and stderr takes the reason for a failure.
//
// Serve returns the exit status to send back: 0, or 1 once it has written the
// reason to stderr. A client still waiting for the transfer to begin is told the
// reason too.
func (s *Server) Serve(command string, channel io.ReadWriter, stderr io.Writer) int {
	if err := s.serve(command, channel); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func (s *Server) serve(command string, channel io.ReadWriter) error {
	c, err := ParseCommand(command)
	if err != nil {
		return err
	}
	c = c.Relative()

	if c.Sink { // scp -t: the client sends files
		if s.ReadOnly {
			// The client waits to hear the target is ready.
			_ = writeFailure(channel, errReadOnly.Error())
			return errReadOnly
		}
		sink, err := NewDirSinkIn(s.Root, c.Paths[0], DirSinkOptions{Preserve: c.Preserve, Atomic: s.Atomic})
		if err != nil {
			_ = writeFailure(channel, "scp: "+c.Paths[0]+": "+describe(err))
			return err
		}
		defer sink.Close()
		return ReceiveAll(channel, channel, ReceiveOptions{Recursive: c.Recursive, MaxSize: s.MaxSize}, sink)
	}

	// scp -f: the client fetches files
	if s.WriteOnly {
		_ = SendFailure(channel, channel, errWriteOnly.Error())
		return errWriteOnly
	}
	fsys := s.Root.FS()
	names, err := c.Expand(fsys)
	if err != nil {
		_ = SendFailure(channel, channel, "scp: "+err.Error())
		return err
	}
	return SendFS(channel, channel, fsys, names, SendOptions{Recursive: c.Recursive, Preserve: c.Preserve})
}
