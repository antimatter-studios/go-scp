package scp_test

import (
	"io"
	"os"

	"github.com/antimatter-studios/go-scp/pkg/scp"
)

// An SSH server answers a session that asked to run an scp command, serving the
// files under one directory and nothing outside it.
func Example_server() {
	root, err := os.OpenRoot(os.TempDir())
	if err != nil {
		return
	}
	defer root.Close()

	answer := func(command string, channel io.ReadWriter, stderr io.Writer) int {
		return scp.Serve(command, channel, stderr, root) // the session's exit status
	}
	_ = answer
}
