package scp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	modTime    = time.Unix(1700000000, 123456000)
	accessTime = time.Unix(1700000100, 0)
)

func TestTimesLineRoundTrips(t *testing.T) {
	h := Header{ModTime: modTime, AccessTime: accessTime}

	var got Header
	err := parseTimes(h.timesLine(), &got)

	if err != nil || !got.ModTime.Equal(modTime) || !got.AccessTime.Equal(accessTime) {
		t.Fatalf("%q read back as (%v, %v, %v)", h.timesLine(), got.ModTime, got.AccessTime, err)
	}
	if h.timesLine() != "T1700000000 123456 1700000100 0" {
		t.Errorf("time line = %q", h.timesLine())
	}
}

func TestTimesLineFillsAMissingTimeWithTheOther(t *testing.T) {
	if got := (Header{ModTime: modTime}).timesLine(); got != "T1700000000 123456 1700000000 123456" {
		t.Errorf("only ModTime: %q", got)
	}
	if got := (Header{AccessTime: accessTime}).timesLine(); got != "T1700000100 0 1700000100 0" {
		t.Errorf("only AccessTime: %q", got)
	}
}

func TestParseTimesRefusesMalformedLines(t *testing.T) {
	for _, line := range []string{"", "T", "X1 0 1 0", "T1 0 1", "T1 0 1 0 5", "T1 0 x 0", "T-1 0 1 0", "T1 1000000 1 0", "T1 0 1 1000000"} {
		var h Header
		if err := parseTimes(line, &h); !errors.Is(err, ErrProtocol) {
			t.Errorf("parseTimes(%q) = %v, want ErrProtocol", line, err)
		}
	}
}

func TestReceivePassesPreservedTimes(t *testing.T) {
	var header Header
	in := strings.NewReader("T1700000000 123456 1700000100 0\nC0644 2 report.txt\nhi\x00")
	var out bytes.Buffer

	err := Receive(in, &out, 0, func(h Header, body io.Reader) error {
		header = h
		_, err := io.ReadAll(body)
		return err
	})

	if err != nil || !header.ModTime.Equal(modTime) || !header.AccessTime.Equal(accessTime) {
		t.Fatalf("Receive = %v, times (%v, %v)", err, header.ModTime, header.AccessTime)
	}
	if out.String() != "\x00\x00\x00\x00" {
		t.Errorf("replies = %q, want the time line acknowledged too", out.String())
	}
}

func TestReceiveRefusesAMalformedTimeLine(t *testing.T) {
	var out bytes.Buffer

	err := Receive(strings.NewReader("T1700000000 0\nC0644 2 report.txt\nhi\x00"), &out, 0, func(Header, io.Reader) error { return nil })

	if !errors.Is(err, ErrProtocol) || out.String() != "\x00\x01malformed time line\n" {
		t.Fatalf("Receive = %v, replies %q", err, out.String())
	}
}

func TestSendWritesTimesAheadOfTheFile(t *testing.T) {
	var out bytes.Buffer
	h := Header{Mode: 0o644, Size: 2, Name: "notes.txt", ModTime: modTime, AccessTime: accessTime}

	err := Send(strings.NewReader("\x00\x00\x00\x00"), &out, h, strings.NewReader("hi"))

	if err != nil || out.String() != "T1700000000 123456 1700000100 0\nC0644 2 notes.txt\nhi\x00" {
		t.Fatalf("Send = %v, wire %q", err, out.String())
	}
}

func TestPreserveUploadAppliesTimesAndMode(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)
	c.Preserve = true
	name := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := c.Upload(t.Context(), name, Header{Mode: 0o640, Size: 3, ModTime: modTime, AccessTime: accessTime}, strings.NewReader("new"))

	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	info, _ := os.Stat(name)
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640 applied to the existing file", info.Mode().Perm())
	}
	if !info.ModTime().Equal(modTime) {
		t.Errorf("mtime = %v, want %v", info.ModTime(), modTime)
	}
}

func TestUploadWithoutPreserveSendsNoTimes(t *testing.T) {
	var cmd string
	c := newTestServer(t, func(command string, stdin io.Reader, stdout, stderr io.Writer) int {
		cmd = command
		return fileServer(command, stdin, stdout, stderr)
	}).dial(t)
	name := filepath.Join(t.TempDir(), "notes.txt")

	if err := c.Upload(t.Context(), name, Header{Mode: 0o644, Size: 1, ModTime: modTime}, strings.NewReader("x")); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if strings.Contains(cmd, "-p") {
		t.Errorf("command = %q, want no -p", cmd)
	}
	if info, _ := os.Stat(name); info.ModTime().Equal(modTime) {
		t.Error("times were applied without Preserve")
	}
}

func TestPreserveDownloadReturnsTheRemoteTimes(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)
	c.Preserve = true
	name := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(name, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, accessTime, modTime); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer

	h, err := c.Download(t.Context(), name, &buf)

	if err != nil || buf.String() != "hello" {
		t.Fatalf("Download = %v, body %q", err, buf.String())
	}
	if !h.ModTime.Equal(modTime.Truncate(time.Microsecond)) {
		t.Errorf("ModTime = %v, want %v", h.ModTime, modTime)
	}
}

func TestPreserveAgainstOpenSSHScp(t *testing.T) {
	c := newTestServer(t, openSSHServer(requireOpenSSHScp(t))).dial(t)
	c.Preserve = true
	name := filepath.Join(awkwardDir(t), "notes.txt")
	if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.Upload(t.Context(), name, Header{Mode: 0o640, Size: 3, ModTime: modTime, AccessTime: accessTime}, strings.NewReader("new")); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	info, _ := os.Stat(name)
	if info.Mode().Perm() != 0o640 || info.ModTime().Unix() != modTime.Unix() {
		t.Fatalf("after upload: mode %v, mtime %v", info.Mode().Perm(), info.ModTime())
	}

	var buf bytes.Buffer
	h, err := c.Download(t.Context(), name, &buf)
	if err != nil || buf.String() != "new" || h.ModTime.Unix() != modTime.Unix() {
		t.Fatalf("Download = (%+v, %v), body %q", h, err, buf.String())
	}
}

func TestServerPreserveAgainstOpenSSHScpClient(t *testing.T) {
	scp := requireOpenSSHScp(t)
	srv := newTestServer(t, fileServer)
	local := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(local, []byte("kept times"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(local, accessTime, modTime); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "stored.txt")

	out, err := exec.Command(scp, "-O", "-p", "-q", "-P", srv.port, "-i", srv.keyPath,
		"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none",
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR",
		local, "tester@"+srv.addr+":"+remote).CombinedOutput()
	if err != nil {
		t.Fatalf("scp -p: %v: %s", err, out)
	}

	info, err := os.Stat(remote)
	if err != nil || info.ModTime().Unix() != modTime.Unix() || info.Mode().Perm() != 0o640 {
		t.Fatalf("stored file: %v, mtime %v, mode %v", err, info.ModTime(), info.Mode().Perm())
	}
}

func TestACancelledContextStartsNothing(t *testing.T) {
	called := false
	c := newTestServer(t, func(string, io.Reader, io.Writer, io.Writer) int {
		called = true
		return 0
	}).dial(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := c.ReadFile(ctx, "/anything")

	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("ReadFile = %v, remote command run %v", err, called)
	}
}

func TestCancellingEndsATransferThatIsWaiting(t *testing.T) {
	c := newTestServer(t, func(_ string, stdin io.Reader, _, _ io.Writer) int {
		io.Copy(io.Discard, stdin) // never answers
		return 0
	}).dial(t)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- c.WriteFile(ctx, "/anything", []byte("x"), 0o644) }()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("WriteFile = %v, want the context's error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling did not end the transfer")
	}
}
