package sftp

import (
	"errors"
	"io"
	"os"
	"testing"

	pkgsftp "github.com/pkg/sftp"
)

// serveEnv, when set, makes the test binary an SFTP server program instead: it
// serves the real filesystem over its standard input and output, starting in the
// directory the variable names, as sftp-server starts in the user's home. The tests
// run it through NewProgramClient.
const serveEnv = "GOSCP_SFTP_SERVE"

func TestMain(m *testing.M) {
	if dir := os.Getenv(serveEnv); dir != "" {
		server, err := pkgsftp.NewServer(stdio{os.Stdin, os.Stdout}, pkgsftp.WithServerWorkingDirectory(dir))
		if err == nil {
			err = server.Serve()
		}
		if err != nil && !errors.Is(err, io.EOF) {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type stdio struct {
	io.Reader
	io.WriteCloser
}

// programClient returns a Client whose sessions are this test binary serving the
// real filesystem, starting in home.
func programClient(t *testing.T, home string) *Client {
	t.Helper()
	t.Setenv(serveEnv, home)
	return NewProgramClient(os.Args[0])
}
