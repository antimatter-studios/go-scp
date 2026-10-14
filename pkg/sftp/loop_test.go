package sftp

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/antimatter-studios/go-scp/pkg/scp"
)

func TestCopyToSkipsALinkBackToADirectoryAbove(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"top/a": "A", "top/sub/b": "B"})
	// top/sub/loop leads back to top, and so does top/twin/loop, through a link
	// to top/sub that does not loop itself.
	for link, to := range map[string]string{"top/sub/loop": "..", "top/twin": "sub"} {
		if err := os.Symlink(to, filepath.Join(local, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
	}
	c := programClient(t, remote)
	c.Recursive = true

	err := c.CopyTo(t.Context(), remote, filepath.Join(local, "top"))

	files, rest := scp.Failures(err)
	if want := []string{"top/sub/loop", "top/twin/loop"}; !reflect.DeepEqual(paths(files), want) || rest != nil {
		t.Errorf("Failures(%v) = %v, %v; want %v and nothing else", err, paths(files), rest, want)
	}
	want := map[string]string{"top/a": "A", "top/sub/b": "B", "top/twin/b": "B"}
	if got := readTree(t, remote); !reflect.DeepEqual(got, want) {
		t.Errorf("copied %v\nwant %v", got, want)
	}
}
