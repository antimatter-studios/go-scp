package scp

import (
	"fmt"
	"io"
	"os"
)

// Serve answers one scp command for an SSH server that handles transfers itself,
// instead of running the scp program: command is what the session asked to run,
// channel is the session's stdin and stdout, and stderr takes the reason for a
// failure. Every path is found inside root, symlinks included, so nothing outside
// it is read or written; a path that climbs out with .. is taken from root's top.
//
// Serve returns the exit status to send back: 0, or 1 once it has written the
// reason to stderr. A client still waiting for the transfer to begin is told the
// reason too.
func Serve(command string, channel io.ReadWriter, stderr io.Writer, root *os.Root) int {
	if err := serve(command, channel, root); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func serve(command string, channel io.ReadWriter, root *os.Root) error {
	c, err := ParseCommand(command)
	if err != nil {
		return err
	}
	c = c.Relative()

	if c.Sink { // scp -t: the client sends files
		sink, err := NewDirSinkIn(root, c.Paths[0], DirSinkOptions{Preserve: c.Preserve})
		if err != nil {
			// The client waits to hear the target is ready.
			_ = writeFailure(channel, "scp: "+c.Paths[0]+": "+describe(err))
			return err
		}
		defer sink.Close()
		return ReceiveAll(channel, channel, ReceiveOptions{Recursive: c.Recursive}, sink)
	}

	fsys := root.FS() // scp -f: the client fetches files
	names, err := c.Expand(fsys)
	if err != nil {
		_ = SendFailure(channel, channel, "scp: "+err.Error())
		return err
	}
	return SendFS(channel, channel, fsys, names, SendOptions{Recursive: c.Recursive, Preserve: c.Preserve})
}
