package scp

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// rw joins a session's stdin and stdout into the one channel Serve takes.
type rw struct {
	io.Reader
	io.Writer
}

// servingRoot starts a test server that answers every command with Serve under a
// new directory, and returns a client for it and the directory.
func servingRoot(t *testing.T) (*Client, string) {
	t.Helper()
	return serving(t, Server{})
}

// serving starts a test server that answers every command with s, serving a new
// directory as its Root, and returns a client for it and the directory.
func serving(t *testing.T, s Server) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	s.Root = root
	c := newTestServer(t, func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		return s.Serve(cmd, rw{stdin, stdout}, stderr)
	}).dial(t)
	return c, dir
}

func TestServeAnswersScpCommands(t *testing.T) {
	c, served := servingRoot(t)
	c.Recursive = true
	local, back := t.TempDir(), t.TempDir()
	writeTree(t, local, tree)

	t.Run("a tree both ways", func(t *testing.T) {
		if err := c.CopyTo(t.Context(), "/", filepath.Join(local, "project")); err != nil {
			t.Fatalf("CopyTo: %v", err)
		}
		if got := readTree(t, served); !reflect.DeepEqual(got, tree) {
			t.Fatalf("served directory holds %v\nwant %v", got, tree)
		}
		if err := c.CopyFrom(t.Context(), back, "/project"); err != nil {
			t.Fatalf("CopyFrom: %v", err)
		}
		if got := readTree(t, back); !reflect.DeepEqual(got, tree) {
			t.Errorf("copied back %v\nwant %v", got, tree)
		}
	})

	t.Run("wildcards", func(t *testing.T) {
		g := *c
		g.Glob = true
		matches := t.TempDir()
		if err := g.CopyFrom(t.Context(), matches, "/project/src/*.go"); err != nil {
			t.Fatalf("CopyFrom: %v", err)
		}
		if got := readTree(t, matches); !reflect.DeepEqual(got, map[string]string{"main.go": "package main"}) {
			t.Errorf("matched %v", got)
		}
	})

	t.Run("a path that climbs out lands inside", func(t *testing.T) {
		if err := c.WriteFile(t.Context(), "../../climbed", []byte("in"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if got, _ := os.ReadFile(filepath.Join(served, "climbed")); string(got) != "in" {
			t.Errorf("climbed holds %q, want it under the served directory", got)
		}
	})
}

// A link inside the served directory cannot lead a copy out of it, either way.
func TestServeKeepsToItsRootThroughLinks(t *testing.T) {
	c, served := servingRoot(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(served, "out")); err != nil {
		t.Fatal(err)
	}

	if err := c.WriteFile(t.Context(), "/out/planted", []byte("x"), 0o644); err == nil {
		t.Error("writing through a link out of the root: want an error")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted")); !os.IsNotExist(err) {
		t.Errorf("a file was written outside the root: %v", err)
	}
	if _, err := c.ReadFile(t.Context(), "/out/secret"); err == nil {
		t.Error("reading through a link out of the root: want an error")
	}
}

func TestServeRefusesWhatIsNotAnScpCommand(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var stderr bytes.Buffer

	if status := Serve("ls -l", rw{strings.NewReader(""), io.Discard}, &stderr, root); status != 1 || !strings.Contains(stderr.String(), "not an scp command") {
		t.Errorf("Serve = %d, stderr %q; want 1 and the reason", status, stderr.String())
	}
}

func TestServeReportsAPatternItCannotRead(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var out, stderr bytes.Buffer

	status := Serve("scp -f '[bad'*", rw{strings.NewReader("\x00"), &out}, &stderr, root)

	if status != 1 || !strings.HasPrefix(out.String(), "\x01") {
		t.Errorf("Serve = %d, replies %q; want 1 and a failure for the client", status, out.String())
	}
}
