package scp

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// watchedReader calls look before each read of r, so a test can see the target
// while a file is still arriving.
type watchedReader struct {
	r    io.Reader
	look func()
}

func (w watchedReader) Read(p []byte) (int, error) {
	w.look()
	return w.r.Read(p)
}

// names returns the names in dir.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestDirSinkAtomicNeverShowsAPartialFile(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"f": "old"})
	target := filepath.Join(dir, "f")
	unchanged := func() {
		if got, _ := os.ReadFile(target); string(got) != "old" {
			t.Errorf("while the file arrives, the target holds %q", got)
		}
	}
	sink := newDirSink(t, dir, DirSinkOptions{Atomic: true, Preserve: true})

	t.Run("a file that fails", func(t *testing.T) {
		err := sink.File(Header{Mode: 0o644, Size: 20, Name: "f"}, watchedReader{strings.NewReader("short"), unchanged})
		if err == nil {
			t.Fatal("a short body: want an error")
		}
		if got, _ := os.ReadFile(target); string(got) != "old" {
			t.Errorf("after a failure the target holds %q, want it as it was", got)
		}
		if got := names(t, dir); !reflect.DeepEqual(got, []string{"f"}) {
			t.Errorf("the directory holds %v, want nothing left behind", got)
		}
	})

	t.Run("a whole file", func(t *testing.T) {
		mtime := time.Unix(1_600_000_000, 0)
		body := watchedReader{iotest.OneByteReader(strings.NewReader("new content")), unchanged}
		if err := sink.File(Header{Mode: 0o640, Size: 11, Name: "f", ModTime: mtime}, body); err != nil {
			t.Fatalf("File: %v", err)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(target); string(got) != "new content" {
			t.Errorf("the target holds %q", got)
		}
		if unixModes && info.Mode().Perm() != 0o640 {
			t.Errorf("mode %v, want 0640", info.Mode().Perm())
		}
		if !info.ModTime().Equal(mtime) {
			t.Errorf("modified %v, want %v", info.ModTime(), mtime)
		}
		if got := names(t, dir); !reflect.DeepEqual(got, []string{"f"}) {
			t.Errorf("the directory holds %v, want nothing left behind", got)
		}
	})

	t.Run("a directory in the way", func(t *testing.T) {
		if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := sink.File(Header{Mode: 0o644, Size: 1, Name: "d"}, strings.NewReader("x")); err == nil {
			t.Error("a file over a directory: want an error")
		}
		if got := names(t, dir); !reflect.DeepEqual(got, []string{"d", "f"}) {
			t.Errorf("the directory holds %v, want nothing left behind", got)
		}
	})
}

func TestDirSinkAtomicResumesThePartKept(t *testing.T) {
	dir := t.TempDir()
	sink := newDirSink(t, dir, DirSinkOptions{Atomic: true, Resume: true})
	h := Header{Mode: 0o644, Size: 10, Name: "r"}

	if err := sink.File(h, strings.NewReader("01234")); err == nil {
		t.Fatal("a short body: want an error")
	}
	if got := names(t, dir); !reflect.DeepEqual(got, []string{".r.partial"}) {
		t.Errorf("after a failure the directory holds %v, want only the part kept", got)
	}
	offset, err := sink.ResumeFrom(h)
	if err != nil || offset != 5 {
		t.Fatalf("ResumeFrom = %d, %v; want 5", offset, err)
	}
	if err := sink.File(h, strings.NewReader("56789")); err != nil {
		t.Fatalf("File: %v", err)
	}
	if got := readTree(t, dir); !reflect.DeepEqual(got, map[string]string{"r": "0123456789"}) {
		t.Errorf("the directory holds %v", got)
	}
}

// The classic protocol's remote scp writes in place, so whatever it writes refuses
// Atomic, and what this side writes honours it.
func TestClassicUploadsRefuseAtomic(t *testing.T) {
	c, other := newTestServer(t, fileServer).dial(t), newTestServer(t, fileServer).dial(t)
	c.Atomic = true
	local, remote, back := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"f": "F"})
	writeTree(t, remote, map[string]string{"g": "G"})

	for name, copyIt := range map[string]func() error{
		"CopyTo":      func() error { return c.CopyTo(t.Context(), remote, filepath.Join(local, "f")) },
		"Upload":      func() error { return c.WriteFile(t.Context(), filepath.Join(remote, "f"), []byte("F"), 0o644) },
		"CopyBetween": func() error { return CopyBetween(t.Context(), c, []string{filepath.Join(remote, "g")}, other, local) },
		"CopyOnHost":  func() error { return c.CopyOnHost(t.Context(), "elsewhere:x", filepath.Join(remote, "g")) },
	} {
		if err := copyIt(); !errors.Is(err, errWritesInPlace) {
			t.Errorf("%s = %v, want it refused", name, err)
		}
	}
	if got := names(t, remote); !reflect.DeepEqual(got, []string{"g"}) {
		t.Errorf("the remote holds %v, want nothing written", got)
	}

	if err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "g")); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if got := readTree(t, back); !reflect.DeepEqual(got, map[string]string{"g": "G"}) {
		t.Errorf("copied back %v", got)
	}
}
