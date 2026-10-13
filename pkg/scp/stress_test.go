package scp

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Inputs at the edges of what either end can be handed: loops, depth and names no
// filesystem takes. Each must end, with an error where one is due, and leave
// nothing outside the target.

// finishes runs f and fails the test if it has not returned within a while.
func finishes(t *testing.T, within time.Duration, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(within):
		t.Fatalf("still running after %v", within)
		return nil
	}
}

// A directory holding a link to itself, sent with -r, is followed as OpenSSH's scp
// follows links. The path grows by one link per level until the system refuses to
// resolve it, and that entry fails; the transfer does not go on forever.
func TestSendFSEndsOnALinkToItsOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	tree := filepath.Join(dir, "tree")
	if err := os.Mkdir(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(tree, "loop")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	err := finishes(t, 30*time.Second, func() error {
		return SendFS(zeros{}, &out, os.DirFS(dir), []string{"tree"}, SendOptions{Recursive: true})
	})

	if err == nil {
		t.Error("SendFS: want the entry the system refused reported")
	}
	if depth := strings.Count(out.String(), "D0755 0 "); depth > 100 {
		t.Errorf("sent %d nested directories, want the loop cut off by the system's link limit", depth)
	}
}

// A sender may nest directories as deep as it likes. The receiving end keeps only
// a count on the way down, and a DirSink refuses a path under its target longer
// than maxPath, as a system with PATH_MAX would.
func TestReceiveAllTakesVeryDeepNesting(t *testing.T) {
	t.Run("any sink", func(t *testing.T) {
		const depth = 100_000
		stream := strings.Repeat("D0755 0 d\n", depth) + "C0644 1 f\nX\x00" + strings.Repeat("E\n", depth)
		err := finishes(t, 30*time.Second, func() error {
			return ReceiveAll(strings.NewReader(stream), io.Discard, ReceiveOptions{Recursive: true}, stepSink{})
		})
		if err != nil {
			t.Errorf("ReceiveAll: %v", err)
		}
	})
	t.Run("DirSink", func(t *testing.T) {
		// Long names reach maxPath in a few dozen levels.
		const depth = 100
		level := "D0755 0 " + strings.Repeat("d", 200) + "\n"
		stream := strings.Repeat(level, depth) + strings.Repeat("E\n", depth)
		base := t.TempDir()
		target := filepath.Join(base, "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		sink := newDirSink(t, target, DirSinkOptions{})
		err := finishes(t, 30*time.Second, func() error {
			return ReceiveAll(strings.NewReader(stream), io.Discard, ReceiveOptions{Recursive: true}, sink)
		})
		if err == nil || !strings.Contains(err.Error(), "longer than 4096 bytes") {
			t.Errorf("ReceiveAll = %v, want the path that grew past maxPath refused", err)
		}
		if entries, _ := os.ReadDir(base); len(entries) != 1 {
			t.Errorf("the target's directory holds %v, want only the target", entries)
		}
	})
}

// Names a filesystem cannot take fail their own file; the rest of the transfer
// goes on and nothing is written anywhere else.
func TestReceiveAllSkipsNamesTheFilesystemRefuses(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	sink := newDirSink(t, target, DirSinkOptions{})
	stream := "C0644 1 nul\x00name\nX\x00" + "C0644 1 ok\nY\x00"

	err := ReceiveAll(strings.NewReader(stream), io.Discard, ReceiveOptions{}, sink)

	if err == nil || !strings.Contains(err.Error(), "nul") {
		t.Errorf("ReceiveAll = %v, want the name with a NUL reported", err)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "ok")); string(got) != "Y" {
		t.Errorf("ok holds %q, want the file after the refused one written", got)
	}
	if entries, _ := os.ReadDir(target); len(entries) != 1 {
		t.Errorf("target holds %v, want only ok", entries)
	}
}
