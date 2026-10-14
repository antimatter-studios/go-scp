package sftp

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/antimatter-studios/go-scp/pkg/scp"
)

// paths returns the Path of each skipped file, in order.
func paths(files []*scp.FileError) []string {
	out := []string{}
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// Over SFTP every file a copy skips can be named, since the copy asks for each
// one by its path.
func TestFailuresNamesEachSkippedFileOverSFTP(t *testing.T) {
	local, remote, back, other := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"dir/ok": "OK"})
	if err := os.Symlink("nowhere", filepath.Join(local, "dir", "dangling")); err != nil {
		t.Fatal(err)
	}
	writeTree(t, remote, map[string]string{"tree/ok": "OK", "tree/big": "too big"})
	c := programClient(t, remote)
	c.Recursive = true

	t.Run("CopyTo", func(t *testing.T) {
		err := c.CopyTo(t.Context(), remote, filepath.Join(local, "missing"), filepath.Join(local, "dir"))

		files, rest := scp.Failures(err)
		if want := []string{"missing", "dir/dangling"}; !reflect.DeepEqual(paths(files), want) || rest != nil {
			t.Errorf("Failures(%v) = %v, %v; want %v and nothing else", err, paths(files), rest, want)
		}
	})

	sources := []string{filepath.Join(remote, "missing"), filepath.Join(remote, "tree")}
	want := []string{"missing", "tree/big"}
	c.MaxSize = 3
	t.Run("CopyFrom", func(t *testing.T) {
		err := c.CopyFrom(t.Context(), back, sources...)

		files, rest := scp.Failures(err)
		if !reflect.DeepEqual(paths(files), want) || rest != nil {
			t.Errorf("Failures(%v) = %v, %v; want %v and nothing else", err, paths(files), rest, want)
		}
	})
	t.Run("CopyBetween", func(t *testing.T) {
		err := CopyBetween(t.Context(), c, sources, c, other)

		files, rest := scp.Failures(err)
		if !reflect.DeepEqual(paths(files), want) || rest != nil {
			t.Errorf("Failures(%v) = %v, %v; want %v and nothing else", err, paths(files), rest, want)
		}
	})
}
