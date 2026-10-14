package sftp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Failures on this side of a copy, and arguments a copy cannot take. Each is the
// copy's own error, or one source's, with the rest copied.

func TestCopiesNeedSources(t *testing.T) {
	c := programClient(t, t.TempDir())
	if err := c.CopyTo(t.Context(), "x"); err == nil {
		t.Error("CopyTo with no sources: want an error")
	}
	if err := c.CopyFrom(t.Context(), t.TempDir()); err == nil {
		t.Error("CopyFrom with no sources: want an error")
	}
}

func TestSeveralSourcesNeedADirectory(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"a": "A", "b": "B"})
	writeTree(t, remote, map[string]string{"a": "A", "b": "B"})
	c := programClient(t, remote)

	if err := c.CopyTo(t.Context(), filepath.Join(remote, "a"), filepath.Join(local, "a"), filepath.Join(local, "b")); err == nil {
		t.Error("CopyTo several files onto a file: want an error")
	}
	if err := c.CopyFrom(t.Context(), filepath.Join(local, "a"), filepath.Join(remote, "a"), filepath.Join(remote, "b")); err == nil {
		t.Error("CopyFrom several files onto a file: want an error")
	}
}

func TestCopyFromNeedsTheTargetsDirectory(t *testing.T) {
	remote := t.TempDir()
	writeTree(t, remote, map[string]string{"a": "A"})

	err := programClient(t, remote).CopyFrom(t.Context(), filepath.Join(t.TempDir(), "missing", "a"), filepath.Join(remote, "a"))
	if err == nil {
		t.Error("CopyFrom into a directory that does not exist: want an error")
	}
}

func TestCopyToSkipsWhatItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	if !unixModes {
		t.Skip("mode 000 locks nothing on Windows")
	}
	local, remote := t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"locked/x": "X", "secret": "S", "ok": "OK"})
	for _, p := range []string{"locked", "secret"} {
		if err := os.Chmod(filepath.Join(local, p), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(filepath.Join(local, p), 0o700) })
	}
	c := programClient(t, remote)
	c.Recursive = true

	err := c.CopyTo(t.Context(), remote, filepath.Join(local, "locked"), filepath.Join(local, "secret"), filepath.Join(local, "ok"))
	if err == nil || !strings.Contains(err.Error(), "locked") || !strings.Contains(err.Error(), "secret") {
		t.Errorf("CopyTo = %v, want both unreadable sources reported", err)
	}
	if got, _ := os.ReadFile(filepath.Join(remote, "ok")); string(got) != "OK" {
		t.Errorf("ok holds %q, want it copied", got)
	}
}

func TestCopyToReportsAnEntryInsideADirectory(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"dir/ok": "OK"})
	if err := os.Symlink("nowhere", filepath.Join(local, "dir", "dangling")); err != nil {
		t.Fatal(err)
	}
	c := programClient(t, remote)
	c.Recursive = true

	err := c.CopyTo(t.Context(), remote, filepath.Join(local, "dir"))
	if err == nil || !strings.Contains(err.Error(), "dangling") {
		t.Errorf("CopyTo = %v, want the dangling link reported", err)
	}
	if got, _ := os.ReadFile(filepath.Join(remote, "dir", "ok")); string(got) != "OK" {
		t.Errorf("dir/ok holds %q, want it copied", got)
	}
}

func TestCopyToWillNotPutADirectoryOverAFile(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"dir/x": "X"})
	writeTree(t, remote, map[string]string{"dir": "a file"})
	c := programClient(t, remote)
	c.Recursive = true

	if err := c.CopyTo(t.Context(), filepath.Join(remote, "dir"), filepath.Join(local, "dir")); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("CopyTo = %v, want the file in the way reported", err)
	}
}

func TestCopyFromReportsWhatThisSideCannotTake(t *testing.T) {
	remote := t.TempDir()
	writeTree(t, remote, map[string]string{"f": "F", "dir/sub/x": "X", "top/x": "X"})
	c := programClient(t, remote)
	c.Recursive = true

	t.Run("a directory where the file goes", func(t *testing.T) {
		back := t.TempDir()
		if err := os.Mkdir(filepath.Join(back, "f"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "f")); err == nil {
			t.Error("want an error")
		}
	})
	t.Run("a file where a directory goes", func(t *testing.T) {
		back := t.TempDir()
		writeTree(t, back, map[string]string{"top": "a file"})
		if err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "top")); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("CopyFrom = %v, want the file in the way reported", err)
		}
	})
	t.Run("a file where a directory inside goes", func(t *testing.T) {
		back := t.TempDir()
		writeTree(t, back, map[string]string{"dir/sub": "a file"})
		if err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "dir")); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("CopyFrom = %v, want the file in the way reported", err)
		}
	})
}

func TestCopyFromReportsSourcesItCannotName(t *testing.T) {
	remote := t.TempDir()
	c := programClient(t, remote)
	c.Glob = true

	err := c.CopyFrom(t.Context(), t.TempDir(), filepath.Join(remote, "*.none"), filepath.Join(remote, "[bad"), "/")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"*.none", "[bad", "not a plain file name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("CopyFrom = %v, want %q reported", err, want)
		}
	}
}

func TestAProgramThatDoesNotServeIsUnavailable(t *testing.T) {
	for name, c := range map[string]*Client{
		"missing":       NewProgramClient(filepath.Join(t.TempDir(), "no-such-program")),
		"exits at once": NewProgramClient("true"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := c.CopyFrom(t.Context(), t.TempDir(), "x"); !errors.Is(err, ErrUnavailable) {
				t.Errorf("CopyFrom = %v, want ErrUnavailable", err)
			}
		})
	}
}
