package scp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// awkwardDir is a directory whose name needs quoting, so every transfer through it
// also proves the path reaches the other end intact.
func awkwardDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "it's a dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The client and this package's own server, end to end over SSH.
func TestClientRoundTripsThroughThePackagesServer(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)
	name := filepath.Join(awkwardDir(t), "notes.txt")
	content := []byte("line one\n\x00binary\xff'quoted' $HOME\n")

	if err := c.WriteFile(t.Context(), name, content, 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := c.ReadFile(t.Context(), name)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("ReadFile = (%q, %v), want %q", got, err, content)
	}
	if info, _ := os.Stat(name); info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", info.Mode().Perm())
	}
}

func TestDownloadReturnsTheHeader(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)
	name := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(name, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer

	h, err := c.Download(t.Context(), name, &buf)

	if err != nil || buf.String() != "hello" || h != (Header{Mode: 0o600, Size: 5, Name: "notes.txt"}) {
		t.Fatalf("Download = (%+v, %v), body %q", h, err, buf.String())
	}
}

func TestReadFileOfAMissingFileIsTheRemotesError(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)

	_, err := c.ReadFile(t.Context(), filepath.Join(t.TempDir(), "absent"))

	var remote *RemoteError
	if !errors.As(err, &remote) || !strings.Contains(remote.Message, "No such file or directory") {
		t.Fatalf("err = %v, want the remote's report", err)
	}
}

func TestUploadOverALongerFileLeavesOnlyTheNewContent(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)
	name := filepath.Join(t.TempDir(), "notes.txt")
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
		t.Errorf("mode = %v, want the existing 0600 kept", info.Mode().Perm())
	}
}

func TestUploadIntoAMissingDirectoryIsTheRemotesError(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)

	err := c.WriteFile(t.Context(), filepath.Join(t.TempDir(), "no-such-dir", "notes.txt"), []byte("x"), 0o644)

	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("err = %v, want the remote's report", err)
	}
}

func TestUploadRefusesAPathWithoutAFileName(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)

	if err := c.WriteFile(t.Context(), "/", []byte("x"), 0o644); err == nil {
		t.Fatal("an upload to / must be refused before anything is sent")
	}
}

func TestDownloadRefusesAFileOverMaxSize(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)
	c.MaxSize = 4
	name := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(name, []byte("more than four"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer

	if _, err := c.Download(t.Context(), name, &buf); err == nil || buf.Len() != 0 {
		t.Fatalf("Download = %v with %d bytes written, want refusal before the body", err, buf.Len())
	}
}

func TestDownloadDoesNotTrustAClaimedSize(t *testing.T) {
	c := newTestServer(t, func(_ string, stdin io.Reader, stdout, _ io.Writer) int {
		stdin.Read(make([]byte, 1)) // ready
		fmt.Fprintf(stdout, "C0644 %d huge\n", int64(1)<<50)
		stdin.Read(make([]byte, 1)) // header accepted
		io.WriteString(stdout, "only this")
		return 0
	}).dial(t)

	if _, err := c.ReadFile(t.Context(), "/huge"); err == nil {
		t.Fatal("a body shorter than its claimed size must fail, without allocating the claim")
	}
}

func TestErrorsCarryTheRemotesStderrWhenScpCannotStart(t *testing.T) {
	c := newTestServer(t, func(_ string, _ io.Reader, _, stderr io.Writer) int {
		io.WriteString(stderr, "sh: 1: scp: not found\n")
		return 127
	}).dial(t)

	_, err := c.ReadFile(t.Context(), "/anything")

	if err == nil || !strings.Contains(err.Error(), "scp: not found") {
		t.Fatalf("err = %v, want the remote's own reason", err)
	}
}

func TestProgramReplacesTheRemoteCommand(t *testing.T) {
	var got string
	c := newTestServer(t, func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		got = cmd
		return fileServer(strings.Replace(cmd, "/opt/bin/scp", "scp", 1), stdin, stdout, stderr)
	}).dial(t)
	c.Program = "/opt/bin/scp"
	name := filepath.Join(t.TempDir(), "notes.txt")

	if err := c.WriteFile(t.Context(), name, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got != "/opt/bin/scp -t "+Quote(name) {
		t.Errorf("command = %q", got)
	}
}
