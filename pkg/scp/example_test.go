package scp_test

import (
	"io"
	"os"
	"path/filepath"

	"github.com/antimatter-studios/go-scp/pkg/scp"
)

// An SSH server answers a session that asked to run an scp command, serving files
// under root.
func Example_server() {
	serve := func(command, root string, stdin io.Reader, stdout io.Writer) error {
		cmd, err := scp.ParseCommand(command)
		if err != nil {
			return err
		}
		cmd = cmd.Relative() // every path now names something under root
		if cmd.Sink {        // scp -t: the peer sends files
			sink, err := scp.NewDirSink(filepath.Join(root, filepath.FromSlash(cmd.Paths[0])), scp.DirSinkOptions{Preserve: cmd.Preserve})
			if err != nil {
				return err
			}
			defer sink.Close()
			return scp.ReceiveAll(stdin, stdout, scp.ReceiveOptions{Recursive: cmd.Recursive}, sink)
		}
		dir, err := os.OpenRoot(root) // scp -f: the peer fetches files
		if err != nil {
			return err
		}
		defer dir.Close()
		fsys := dir.FS() // confined to root, symlinks included
		names, err := cmd.Expand(fsys)
		if err != nil {
			return err
		}
		return scp.SendFS(stdin, stdout, fsys, names, scp.SendOptions{Recursive: cmd.Recursive, Preserve: cmd.Preserve})
	}
	_ = serve
}
