package scp

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests hold both halves of the package to OpenSSH's own scp, the program
// they exist to talk to. They are skipped where it is not installed.

func requireOpenSSHScp(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows' OpenSSH scp names a file it sends by its whole path; the remote ends these tests stand in for run a Unix scp")
	}
	scp, err := exec.LookPath("scp")
	if err != nil {
		t.Skip("OpenSSH scp is not installed")
	}
	return scp
}

// openSSHServer answers scp commands by starting the real scp program on them,
// directly rather than through a shell: the flags and paths come from ParseCommand,
// and unquoted wildcards are expanded here, as the remote shell would.
func openSSHServer(scp string) handler {
	return func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		c, err := ParseCommand(cmd)
		if err != nil {
			io.WriteString(stderr, err.Error()+"\n")
			return 127
		}
		args := []string{}
		for flag, on := range map[string]bool{"-r": c.Recursive, "-p": c.Preserve, "-d": c.TargetIsDir} {
			if on {
				args = append(args, flag)
			}
		}
		if c.Sink {
			args = append(args, "-t", "--")
		} else {
			args = append(args, "-f", "--")
		}
		for i, p := range c.Paths {
			if matches, _ := filepath.Glob(p); c.Patterns[i] && len(matches) > 0 {
				args = append(args, matches...)
				continue
			}
			args = append(args, p)
		}
		run := exec.Command(scp, args...)
		run.Stdout, run.Stderr = stdout, stderr
		// sshd ends the session when scp exits. Copying the session into scp's
		// stdin in a goroutine of our own keeps Wait from waiting on it: that copy
		// only returns once the client closes its side, and a client fetching
		// several files waits for this side to close first.
		in, err := run.StdinPipe()
		if err != nil {
			return 127
		}
		go func() {
			io.Copy(in, stdin)
			in.Close()
		}()
		if err := run.Run(); run.ProcessState == nil {
			io.WriteString(stderr, err.Error()+"\n")
			return 127
		}
		return run.ProcessState.ExitCode()
	}
}

func TestClientAgainstOpenSSHScp(t *testing.T) {
	c := newTestServer(t, openSSHServer(requireOpenSSHScp(t))).dial(t)
	dir := awkwardDir(t)

	t.Run("write then read", func(t *testing.T) {
		name := filepath.Join(dir, "notes.txt")
		content := []byte("line one\n\x00binary\xff'quoted' $HOME\n")
		if err := c.WriteFile(t.Context(), name, content, 0o640); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got, err := c.ReadFile(t.Context(), name)
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("ReadFile = (%q, %v)", got, err)
		}
	})

	t.Run("overwrite keeps the file's mode and length is the new one", func(t *testing.T) {
		name := filepath.Join(dir, "existing")
		if err := os.WriteFile(name, []byte("a much longer old file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := c.WriteFile(t.Context(), name, []byte("short"), 0o644); err != nil {
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
		_, err := c.ReadFile(t.Context(), filepath.Join(dir, "absent"))
		var remote *RemoteError
		if !errors.As(err, &remote) || !strings.Contains(remote.Message, "No such file") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a directory is refused", func(t *testing.T) {
		if _, err := c.ReadFile(t.Context(), dir); err == nil {
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
