package scp

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests hold both halves of the package to OpenSSH's own scp, the program
// they exist to talk to. They are skipped where it is not installed.

func requireOpenSSHScp(t *testing.T) string {
	t.Helper()
	scp, err := exec.LookPath("scp")
	if err != nil {
		t.Skip("OpenSSH scp is not installed")
	}
	return scp
}

// openSSHServer answers scp commands by starting the real scp program on them,
// directly rather than through a shell.
func openSSHServer(scp string) handler {
	return func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		flag, name, ok := scpCommand(cmd)
		if !ok {
			io.WriteString(stderr, "not an scp command\n")
			return 127
		}
		c := exec.Command(scp, flag, name)
		c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
		err := c.Run()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		if err != nil {
			return 127
		}
		return 0
	}
}

func TestClientAgainstOpenSSHScp(t *testing.T) {
	c := newTestServer(t, openSSHServer(requireOpenSSHScp(t))).dial(t)
	dir := awkwardDir(t)

	t.Run("write then read", func(t *testing.T) {
		name := filepath.Join(dir, "notes.txt")
		content := []byte("line one\n\x00binary\xff'quoted' $(not run)\n")
		if err := c.WriteFile(name, content, 0o640); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got, err := c.ReadFile(name)
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("ReadFile = (%q, %v)", got, err)
		}
	})

	t.Run("overwrite keeps the file's mode and length is the new one", func(t *testing.T) {
		name := filepath.Join(dir, "existing")
		if err := os.WriteFile(name, []byte("a much longer old file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := c.WriteFile(name, []byte("short"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if got, _ := os.ReadFile(name); string(got) != "short" {
			t.Errorf("file holds %q", got)
		}
		if info, _ := os.Stat(name); info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600 kept", info.Mode().Perm())
		}
	})

	t.Run("a missing file is scp's own report", func(t *testing.T) {
		_, err := c.ReadFile(filepath.Join(dir, "absent"))
		var remote *RemoteError
		if !errors.As(err, &remote) || !strings.Contains(remote.Message, "No such file") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a directory is refused", func(t *testing.T) {
		if _, err := c.ReadFile(dir); err == nil {
			t.Fatal("reading a directory must fail")
		}
	})
}

// OpenSSH's scp client, run with -O so it speaks this protocol rather than SFTP,
// against this package's server half.
func TestServerAgainstOpenSSHScpClient(t *testing.T) {
	scp := requireOpenSSHScp(t)
	srv := newTestServer(t, fileServer)
	remoteDir := t.TempDir()
	localDir := t.TempDir()

	run := func(t *testing.T, args ...string) {
		t.Helper()
		base := []string{
			"-O", "-q", "-P", srv.port, "-i", srv.keyPath,
			"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none",
			"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR",
		}
		out, err := exec.Command(scp, append(base, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("scp %v: %v: %s", args, err, out)
		}
	}

	content := []byte("from the OpenSSH client\n\x00\xff")
	local := filepath.Join(localDir, "upload.txt")
	if err := os.WriteFile(local, content, 0o640); err != nil {
		t.Fatal(err)
	}

	remote := filepath.Join(remoteDir, "stored.txt")
	run(t, local, "tester@"+srv.addr+":"+remote)
	if got, _ := os.ReadFile(remote); !bytes.Equal(got, content) {
		t.Fatalf("server stored %q, want %q", got, content)
	}

	back := filepath.Join(localDir, "download.txt")
	run(t, "tester@"+srv.addr+":"+remote, back)
	if got, _ := os.ReadFile(back); !bytes.Equal(got, content) {
		t.Fatalf("client fetched %q, want %q", got, content)
	}
}
