package scp

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// loopingTree writes a tree under dir whose top/sub/loop links back to top, and
// whose top/twin links to top/sub, so twin/loop leads back to top as well.
func loopingTree(t *testing.T, dir string) {
	t.Helper()
	writeTree(t, dir, map[string]string{"top/a": "A", "top/sub/b": "B"})
	for link, to := range map[string]string{"top/sub/loop": "..", "top/twin": "sub"} {
		if err := os.Symlink(to, filepath.Join(dir, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
	}
}

// What a looping tree copies as: each link back is skipped, and a link that does
// not loop is copied like the directory it leads to.
var unlooped = map[string]string{"top/a": "A", "top/sub/b": "B", "top/twin/b": "B"}

func TestSendFSSkipsALinkBackToADirectoryAbove(t *testing.T) {
	t.Run("CopyTo", func(t *testing.T) {
		c := newTestServer(t, fileServer).dial(t)
		c.Recursive = true
		local, remote := t.TempDir(), t.TempDir()
		loopingTree(t, local)

		err := c.CopyTo(t.Context(), remote, filepath.Join(local, "top"))

		files, rest := Failures(err)
		if want := []string{"top/sub/loop", "top/twin/loop"}; !reflect.DeepEqual(paths(files), want) || rest != nil {
			t.Errorf("Failures(%v) = %v, %v; want %v and nothing else", err, paths(files), rest, want)
		}
		if got := readTree(t, remote); !reflect.DeepEqual(got, unlooped) {
			t.Errorf("copied %v\nwant %v", got, unlooped)
		}
	})

	t.Run("served", func(t *testing.T) {
		c, served := servingRoot(t)
		c.Recursive = true
		loopingTree(t, served)
		back := t.TempDir()

		err := c.CopyFrom(t.Context(), back, "/top")

		if files, rest := Failures(err); len(files) != 2 || rest != nil {
			t.Errorf("Failures(%v) = %v, %v; want both links back reported", err, paths(files), rest)
		}
		if got := readTree(t, back); !reflect.DeepEqual(got, unlooped) {
			t.Errorf("copied %v\nwant %v", got, unlooped)
		}
	})
}
