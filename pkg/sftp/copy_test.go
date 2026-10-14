package sftp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var modTime = time.Unix(1_600_000_000, 0)

// writeTree creates files under dir: each key a slash-separated path, each value
// its content.
func writeTree(t testing.TB, dir string, files map[string]string) {
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

// readTree returns every file under dir, as writeTree takes them.
func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		got[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

var tree = map[string]string{
	"project/README":            "read me",
	"project/src/main.go":       "package main",
	"project/src/util/a.go":     "package util",
	"project/docs/it's here.md": "quoted name",
	"project/empty":             "",
}

func TestCopyToAndBackATree(t *testing.T) {
	local, remote, back := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, local, tree)
	c := programClient(t, remote)
	c.Recursive = true

	if err := c.CopyTo(t.Context(), remote, filepath.Join(local, "project")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if got := readTree(t, remote); !reflect.DeepEqual(got, tree) {
		t.Fatalf("remote holds %v\nwant %v", got, tree)
	}
	if err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "project")); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if got := readTree(t, back); !reflect.DeepEqual(got, tree) {
		t.Errorf("copied back %v\nwant %v", got, tree)
	}
}

func TestCopyToOneFileUnderANewName(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"a.txt": "A"})

	if err := programClient(t, remote).CopyTo(t.Context(), filepath.Join(remote, "b.txt"), filepath.Join(local, "a.txt")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if got := readTree(t, remote); !reflect.DeepEqual(got, map[string]string{"b.txt": "A"}) {
		t.Errorf("remote holds %v", got)
	}
}

// The server creates a file with a mode of its own; a private file must not land
// readable by others.
func TestCopyToKeepsANewFilePrivate(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	key := filepath.Join(local, "key")
	if err := os.WriteFile(key, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(local, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh"), 0o777); err != nil {
		t.Fatal(err)
	}

	if err := programClient(t, remote).CopyTo(t.Context(), remote, key, script); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	for name, want := range map[string]os.FileMode{"key": 0o600, "run.sh": 0o755} {
		if info, err := os.Stat(filepath.Join(remote, name)); err != nil || (unixModes && info.Mode().Perm() != want) {
			t.Errorf("%s: mode %v (%v), want %v", name, info.Mode().Perm(), err, want)
		}
	}
}

func TestCopyToKeepsAnExistingFilesMode(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"a": "new content"})
	existing := filepath.Join(remote, "a")
	if err := os.WriteFile(existing, []byte("an older and longer file"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := programClient(t, remote).CopyTo(t.Context(), existing, filepath.Join(local, "a")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if got, _ := os.ReadFile(existing); string(got) != "new content" {
		t.Errorf("file holds %q", got)
	}
	if info, _ := os.Stat(existing); unixModes && info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640 kept", info.Mode().Perm())
	}
}

func TestPreserveKeepsModesAndTimesBothWays(t *testing.T) {
	local, remote, back := t.TempDir(), t.TempDir(), t.TempDir()
	dir := filepath.Join(local, "dir")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o604); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, dir} {
		if err := os.Chtimes(p, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
	c := programClient(t, remote)
	c.Recursive, c.Preserve = true, true

	if err := c.CopyTo(t.Context(), remote, dir); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "dir")); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	for _, root := range []string{remote, back} {
		for p, mode := range map[string]os.FileMode{"dir": 0o750, "dir/f": 0o604} {
			info, err := os.Stat(filepath.Join(root, p))
			if err != nil || (unixModes && info.Mode().Perm() != mode) || !info.ModTime().Equal(modTime) {
				t.Errorf("%s/%s: %v %v (%v), want %v %v", root, p, info.Mode().Perm(), info.ModTime(), err, mode, modTime)
			}
		}
	}
}

func TestCopyFromWithWildcards(t *testing.T) {
	remote, back := t.TempDir(), t.TempDir()
	writeTree(t, remote, map[string]string{"logs/a.txt": "A", "logs/b.txt": "B", "logs/c.log": "C"})
	c := programClient(t, remote)
	c.Glob = true

	// SFTP paths are slash-separated, which a Windows path is not.
	if err := c.CopyFrom(t.Context(), back, filepath.ToSlash(filepath.Join(remote, "logs", "*.txt"))); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if got := readTree(t, back); !reflect.DeepEqual(got, map[string]string{"a.txt": "A", "b.txt": "B"}) {
		t.Errorf("copied %v", got)
	}
}

// A leading ~ is the login directory, where the session starts, as a shell would
// expand it for scp's remote paths.
func TestTildeIsTheLoginDirectory(t *testing.T) {
	local, home, back := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"a": "A"})
	c := programClient(t, home)

	for _, target := range []string{"~", "~/", "~/b"} {
		if err := c.CopyTo(t.Context(), target, filepath.Join(local, "a")); err != nil {
			t.Fatalf("CopyTo %s: %v", target, err)
		}
	}
	if got := readTree(t, home); !reflect.DeepEqual(got, map[string]string{"a": "A", "b": "A"}) {
		t.Fatalf("home holds %v", got)
	}
	if err := c.CopyFrom(t.Context(), back, "~/a", "b"); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if got := readTree(t, back); !reflect.DeepEqual(got, map[string]string{"a": "A", "b": "A"}) {
		t.Errorf("copied %v", got)
	}
}

func TestEachSourceThatFailsIsReportedAndTheRestCopied(t *testing.T) {
	local, remote, back := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"ok": "OK", "dir/x": "X"})
	writeTree(t, remote, map[string]string{"ok": "OK", "dir/x": "X"})
	c := programClient(t, remote)

	err := c.CopyTo(t.Context(), t.TempDir(), filepath.Join(local, "missing"), filepath.Join(local, "dir"), filepath.Join(local, "ok"))
	if err == nil || !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("CopyTo = %v, want the missing file and the directory reported", err)
	}
	err = c.CopyFrom(t.Context(), back, filepath.Join(remote, "missing"), filepath.Join(remote, "dir"), filepath.Join(remote, "ok"))
	if err == nil || !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("CopyFrom = %v, want the missing file and the directory reported", err)
	}
	if got := readTree(t, back); !reflect.DeepEqual(got, map[string]string{"ok": "OK"}) {
		t.Errorf("copied %v, want the file that could be", got)
	}
}
