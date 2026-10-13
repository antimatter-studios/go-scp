package scp

import (
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeTree creates files under dir from a map of slash-separated paths to contents.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// readTree returns every file under dir as slash-separated paths to contents.
func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

var tree = map[string]string{
	"project/README":            "read me",
	"project/src/main.go":       "package main",
	"project/src/util/a.go":     "package util",
	"project/docs/it's here.md": "quoted name",
	"project/empty":             "",
}

// eachRemote runs a test against both stand-ins for the remote end: this package's
// server half, and OpenSSH's scp where it is installed.
func eachRemote(t *testing.T, test func(t *testing.T, c *Client)) {
	t.Run("go server", func(t *testing.T) { test(t, newTestServer(t, fileServer).dial(t)) })
	t.Run("openssh scp", func(t *testing.T) { test(t, newTestServer(t, openSSHServer(requireOpenSSHScp(t))).dial(t)) })
}

func TestCopyToAndFromRoundTripsATree(t *testing.T) {
	eachRemote(t, func(t *testing.T, c *Client) {
		c.Recursive = true
		local := t.TempDir()
		writeTree(t, local, tree)
		remote := awkwardDir(t)
		back := t.TempDir()

		if err := c.CopyTo(t.Context(), remote, filepath.Join(local, "project")); err != nil {
			t.Fatalf("CopyTo: %v", err)
		}
		if err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "project")); err != nil {
			t.Fatalf("CopyFrom: %v", err)
		}

		if got := readTree(t, back); !reflect.DeepEqual(got, tree) {
			t.Errorf("round trip = %v\nwant %v", got, tree)
		}
	})
}

func TestCopyToSeveralFilesIntoADirectory(t *testing.T) {
	eachRemote(t, func(t *testing.T, c *Client) {
		local := t.TempDir()
		writeTree(t, local, map[string]string{"a.txt": "A", "b.txt": "B"})
		remote := t.TempDir()

		if err := c.CopyTo(t.Context(), remote, filepath.Join(local, "a.txt"), filepath.Join(local, "b.txt")); err != nil {
			t.Fatalf("CopyTo: %v", err)
		}
		if got := readTree(t, remote); !reflect.DeepEqual(got, map[string]string{"a.txt": "A", "b.txt": "B"}) {
			t.Errorf("remote holds %v", got)
		}
	})
}

func TestCopyToSeveralFilesNeedsADirectory(t *testing.T) {
	eachRemote(t, func(t *testing.T, c *Client) {
		local := t.TempDir()
		writeTree(t, local, map[string]string{"a": "A", "b": "B"})
		notADir := filepath.Join(t.TempDir(), "file")
		writeTree(t, filepath.Dir(notADir), map[string]string{"file": "x"})

		if err := c.CopyTo(t.Context(), notADir, filepath.Join(local, "a"), filepath.Join(local, "b")); err == nil {
			t.Fatal("several files into a file must fail")
		}
	})
}

func TestCopyToADirectoryWithoutRecursiveIsSkipped(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)
	local := t.TempDir()
	writeTree(t, local, map[string]string{"dir/x": "X", "file": "F"})
	remote := t.TempDir()

	err := c.CopyTo(t.Context(), remote, filepath.Join(local, "dir"), filepath.Join(local, "file"))

	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want the directory reported", err)
	}
	if got := readTree(t, remote); !reflect.DeepEqual(got, map[string]string{"file": "F"}) {
		t.Errorf("remote holds %v, want only the file", got)
	}
}

func TestCopyToCarriesOnPastAMissingSource(t *testing.T) {
	eachRemote(t, func(t *testing.T, c *Client) {
		local := t.TempDir()
		writeTree(t, local, map[string]string{"here": "H"})
		remote := t.TempDir()

		err := c.CopyTo(t.Context(), remote, filepath.Join(local, "missing"), filepath.Join(local, "here"))

		if err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("err = %v, want the missing source reported", err)
		}
		if got := readTree(t, remote); !reflect.DeepEqual(got, map[string]string{"here": "H"}) {
			t.Errorf("remote holds %v", got)
		}
	})
}

func TestCopyFromSeveralFilesAndAGlob(t *testing.T) {
	eachRemote(t, func(t *testing.T, c *Client) {
		c.Glob = true
		remote := t.TempDir()
		writeTree(t, remote, map[string]string{"logs/a.txt": "A", "logs/b.txt": "B", "logs/c.log": "C", "notes": "N"})
		local := t.TempDir()

		if err := c.CopyFrom(t.Context(), local, filepath.Join(remote, "logs", "*.txt"), filepath.Join(remote, "notes")); err != nil {
			t.Fatalf("CopyFrom: %v", err)
		}
		want := map[string]string{"a.txt": "A", "b.txt": "B", "notes": "N"}
		if got := readTree(t, local); !reflect.DeepEqual(got, want) {
			t.Errorf("local holds %v, want %v", got, want)
		}
	})
}

func TestCopyFromSeveralSourcesNeedsADirectory(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)

	err := c.CopyFrom(t.Context(), filepath.Join(t.TempDir(), "not-there"), "/a", "/b")

	if err == nil || !strings.Contains(err.Error(), "existing directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestCopyFromIntoAFileName(t *testing.T) {
	eachRemote(t, func(t *testing.T, c *Client) {
		remote := t.TempDir()
		writeTree(t, remote, map[string]string{"notes": "N"})
		target := filepath.Join(t.TempDir(), "renamed")

		if err := c.CopyFrom(t.Context(), target, filepath.Join(remote, "notes")); err != nil {
			t.Fatalf("CopyFrom: %v", err)
		}
		if data, _ := os.ReadFile(target); string(data) != "N" {
			t.Errorf("target holds %q", data)
		}
	})
}

// unrequestedNameServer is a remote that sends a file under a name that was not requested.
func unrequestedNameServer(name string) handler {
	return func(_ string, stdin io.Reader, stdout, _ io.Writer) int {
		ack := make([]byte, 1)
		stdin.Read(ack) // ready
		io.WriteString(stdout, "C0644 1 "+name+"\n")
		if stdin.Read(ack); ack[0] != 0 {
			return 1 // the name was refused
		}
		io.WriteString(stdout, "X\x00")
		stdin.Read(ack) // stored
		return 0
	}
}

func TestCopyFromRefusesANameThatWasNotAskedFor(t *testing.T) {
	c := newTestServer(t, unrequestedNameServer("settings.txt")).dial(t)
	local := t.TempDir()

	err := c.CopyFrom(t.Context(), local, "/remote/notes")

	if err == nil || !strings.Contains(err.Error(), "not asked for") {
		t.Fatalf("err = %v", err)
	}
	if got := readTree(t, local); len(got) != 0 {
		t.Errorf("wrote %v", got)
	}
}

func TestCopyFromAnyNamesAcceptsOtherNames(t *testing.T) {
	c := newTestServer(t, unrequestedNameServer("other")).dial(t)
	c.AnyNames = true
	local := t.TempDir()

	if err := c.CopyFrom(t.Context(), local, "/remote/notes"); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if got := readTree(t, local); !reflect.DeepEqual(got, map[string]string{"other": "X"}) {
		t.Errorf("local holds %v", got)
	}
}

func TestCopyFromNeverAcceptsANameThatLeavesTheTarget(t *testing.T) {
	c := newTestServer(t, unrequestedNameServer("../outside.txt")).dial(t)
	c.AnyNames = true
	parent := t.TempDir()
	local := filepath.Join(parent, "target")
	if err := os.Mkdir(local, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := c.CopyFrom(t.Context(), local, "/remote/x"); err == nil {
		t.Fatal("a name with a directory part must be refused")
	}
	if _, err := os.Stat(filepath.Join(parent, "outside.txt")); err == nil {
		t.Error("a file was written outside the target")
	}
}

func TestCopyNeedsSources(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)

	if err := c.CopyTo(t.Context(), "/x"); err == nil {
		t.Error("CopyTo with no sources succeeded")
	}
	if err := c.CopyFrom(t.Context(), t.TempDir()); err == nil {
		t.Error("CopyFrom with no sources succeeded")
	}
}

func TestDirSinkStaysInsideTheTargetThroughASymlink(t *testing.T) {
	outside := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(target, "link")); err != nil {
		t.Skip("symlinks not available:", err)
	}
	sink, err := NewDirSink(target, DirSinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	if err := sink.EnterDir(Header{Mode: 0o755, Name: "link"}); err == nil {
		err = sink.File(Header{Mode: 0o644, Size: 1, Name: "through-link.txt"}, strings.NewReader("x"))
		if err == nil {
			t.Fatal("a write through a symlink out of the target succeeded")
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "through-link.txt")); err == nil {
		t.Error("a file was written outside the target")
	}
}

func TestDirSinkTakesOnlyOneEntryWhenTheTargetIsNotADirectory(t *testing.T) {
	target := filepath.Join(t.TempDir(), "single")
	sink, err := NewDirSink(target, DirSinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	if err := sink.File(Header{Mode: 0o644, Size: 1, Name: "a"}, strings.NewReader("A")); err != nil {
		t.Fatalf("first entry: %v", err)
	}
	if err := sink.File(Header{Mode: 0o644, Size: 1, Name: "b"}, strings.NewReader("B")); err == nil {
		t.Fatal("a second entry into a file target succeeded")
	}
}

func TestDirSinkRefusesAFileWhereADirectoryIsSent(t *testing.T) {
	target := t.TempDir()
	writeTree(t, target, map[string]string{"name": "a file"})
	sink, err := NewDirSink(target, DirSinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	if err := sink.EnterDir(Header{Mode: 0o755, Name: "name"}); err == nil {
		t.Fatal("entering a file as a directory succeeded")
	}
	if err := sink.LeaveDir(); err == nil {
		t.Fatal("leaving a directory that was never entered succeeded")
	}
}

func TestDirSinkPreserveAppliesDirectoryModeAndTimesOnLeaving(t *testing.T) {
	target := t.TempDir()
	sink, err := NewDirSink(target, DirSinkOptions{Preserve: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	if err := sink.EnterDir(Header{Mode: 0o750, Name: "dir", ModTime: modTime}); err != nil {
		t.Fatal(err)
	}
	if err := sink.File(Header{Mode: 0o600, Size: 1, Name: "f", ModTime: modTime}, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := sink.LeaveDir(); err != nil {
		t.Fatal(err)
	}

	dir, _ := os.Stat(filepath.Join(target, "dir"))
	file, _ := os.Stat(filepath.Join(target, "dir", "f"))
	if dir.Mode().Perm() != 0o750 || dir.ModTime().Unix() != modTime.Unix() {
		t.Errorf("dir: mode %v, mtime %v", dir.Mode().Perm(), dir.ModTime())
	}
	if file.Mode().Perm() != 0o600 || file.ModTime().Unix() != modTime.Unix() {
		t.Errorf("file: mode %v, mtime %v", file.Mode().Perm(), file.ModTime())
	}
}

// OpenSSH's scp client, run with -O -r, against this package's server half.
func TestServerTreeAgainstOpenSSHScpClient(t *testing.T) {
	scp := requireOpenSSHScp(t)
	srv := newTestServer(t, fileServer)
	local := t.TempDir()
	writeTree(t, local, tree)
	remote := t.TempDir()
	back := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		base := []string{"-O", "-r", "-q", "-P", srv.port, "-i", srv.keyPath,
			"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none",
			"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR"}
		if out, err := exec.Command(scp, append(base, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("scp %v: %v: %s", args, err, out)
		}
	}

	run(filepath.Join(local, "project"), "tester@"+srv.addr+":"+remote)
	run("tester@"+srv.addr+":"+filepath.Join(remote, "project"), back)

	if got := readTree(t, back); !reflect.DeepEqual(got, tree) {
		t.Errorf("round trip = %v\nwant %v", got, tree)
	}
}

func TestCopyFromNeedsTheTargetsDirectory(t *testing.T) {
	target := filepath.Join(t.TempDir(), "missing", "x")

	if err := scripted().CopyFrom(t.Context(), target, "/x"); err == nil {
		t.Error("a target whose directory does not exist: want an error")
	}
}
