package scp

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// paths returns the Path of each skipped file, in order.
func paths(files []*FileError) []string {
	out := []string{}
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func TestFailuresNamesEachSkippedFile(t *testing.T) {
	t.Run("CopyTo", func(t *testing.T) {
		c := newTestServer(t, fileServer).dial(t)
		c.Recursive = true
		local, remote := t.TempDir(), t.TempDir()
		writeTree(t, local, map[string]string{"dir/ok": "OK"})
		if err := os.Symlink("nowhere", filepath.Join(local, "dir", "dangling")); err != nil {
			t.Fatal(err)
		}

		err := c.CopyTo(t.Context(), remote, filepath.Join(local, "missing"), filepath.Join(local, "dir"))

		files, rest := Failures(err)
		if want := []string{"missing", "dir/dangling"}; !reflect.DeepEqual(paths(files), want) || rest != nil {
			t.Errorf("Failures(%v) = %v, %v; want %v and nothing else", err, paths(files), rest, want)
		}
		if got := readTree(t, remote); !reflect.DeepEqual(got, map[string]string{"dir/ok": "OK"}) {
			t.Errorf("remote holds %v, want the rest copied", got)
		}
	})

	t.Run("CopyFrom", func(t *testing.T) {
		c := newTestServer(t, fileServer).dial(t)
		c.Recursive, c.MaxSize = true, 3
		remote, back := t.TempDir(), t.TempDir()
		writeTree(t, remote, map[string]string{"dir/ok": "OK", "dir/big": "too big"})

		err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "missing"), filepath.Join(remote, "dir"))

		// Only the remote's message names a source it cannot read.
		files, rest := Failures(err)
		if want := []string{"", "dir/big"}; !reflect.DeepEqual(paths(files), want) || rest != nil {
			t.Errorf("Failures(%v) = %v, %v; want %v and nothing else", err, paths(files), rest, want)
		}
		if !strings.Contains(files[0].Error(), "No such file") {
			t.Errorf("the missing source is reported as %q", files[0])
		}
		var remoteErr *RemoteError
		if !errors.As(files[0], &remoteErr) {
			t.Errorf("%v does not unwrap to the remote's own error", files[0])
		}
	})

	t.Run("CopyBetween", func(t *testing.T) {
		eachPair(t, func(t *testing.T, src, dst *Client) {
			from, to := t.TempDir(), t.TempDir()
			writeTree(t, from, map[string]string{"ok": "OK"})

			err := CopyBetween(t.Context(), src, []string{filepath.Join(from, "missing"), filepath.Join(from, "ok")}, dst, to)

			files, rest := Failures(err)
			if len(files) != 1 || rest != nil {
				t.Errorf("Failures(%v) = %v, %v; want the missing source and nothing else", err, paths(files), rest)
			}
		})
	})

	// The second host refuses a file inside a directory, and the copy names it by
	// its whole path in the copy, not only by the name the second host was sent.
	t.Run("CopyBetween refused by the second host", func(t *testing.T) {
		eachPair(t, func(t *testing.T, src, dst *Client) {
			src.Recursive = true
			from, to := t.TempDir(), t.TempDir()
			writeTree(t, from, map[string]string{"d/x": "X", "d/y": "Y"})
			if err := os.MkdirAll(filepath.Join(to, "d", "x"), 0o755); err != nil {
				t.Fatal(err)
			}

			err := CopyBetween(t.Context(), src, []string{filepath.Join(from, "d")}, dst, to)

			files, rest := Failures(err)
			if want := []string{"d/x"}; !reflect.DeepEqual(paths(files), want) || rest != nil {
				t.Errorf("Failures(%v) = %v, %v; want %v and nothing else", err, paths(files), rest, want)
			}
		})
	})

	t.Run("a copy that stops", func(t *testing.T) {
		c := newTestServer(t, fileServer).dial(t)
		notADir := filepath.Join(t.TempDir(), "file")
		writeTree(t, filepath.Dir(notADir), map[string]string{"file": "x"})

		err := c.CopyFrom(t.Context(), notADir, "/a", "/b")

		if files, rest := Failures(err); len(files) != 0 || rest == nil {
			t.Errorf("Failures(%v) = %v, %v; want only what stopped it", err, paths(files), rest)
		}
	})

	t.Run("no error", func(t *testing.T) {
		if files, rest := Failures(nil); files != nil || rest != nil {
			t.Errorf("Failures(nil) = %v, %v", files, rest)
		}
	})
}
