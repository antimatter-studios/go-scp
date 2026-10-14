package sftp

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	pkgsftp "github.com/pkg/sftp"
)

// remoteNames returns the names in dir on f's in-memory filesystem.
func remoteNames(t *testing.T, c *Client, dir string) []string {
	t.Helper()
	var out []string
	err := c.with(t.Context(), func(conn *pkgsftp.Client) error {
		entries, err := conn.ReadDir(dir)
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func TestAtomicCopyToReplacesTheFileWhole(t *testing.T) {
	local, remote, other := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"f": "new content"})
	writeTree(t, remote, map[string]string{"f": "old"})
	writeTree(t, other, map[string]string{"f": "old"})
	c := programClient(t, remote)
	c.Atomic = true
	// Progress is told as the file starts and as its bytes go, which is while it
	// is arriving.
	watching := filepath.Join(remote, "f")
	c.Progress = func(string, int64, int64) {
		if got, _ := os.ReadFile(watching); string(got) != "old" {
			t.Errorf("while the file arrives, the target holds %q", got)
		}
	}

	if err := c.CopyTo(t.Context(), remote, filepath.Join(local, "f")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	watching = filepath.Join(other, "f")
	if err := CopyBetween(t.Context(), c, []string{filepath.Join(remote, "f")}, c, other); err != nil {
		t.Fatalf("CopyBetween: %v", err)
	}
	for _, dir := range []string{remote, other} {
		if got := readTree(t, dir); !reflect.DeepEqual(got, map[string]string{"f": "new content"}) {
			t.Errorf("%s holds %v, want only the new file", dir, got)
		}
	}
}

func TestAtomicCopyToLeavesTheTargetOnAFailure(t *testing.T) {
	f := newFaults()
	c := faultyClient(f)
	local := t.TempDir()
	writeTree(t, local, map[string]string{"f": "old"})
	if err := c.CopyTo(t.Context(), "/f", filepath.Join(local, "f")); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("0123456789"), 10_000)
	if err := os.WriteFile(filepath.Join(local, "f"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	f.failWritesFrom = 40_000
	c.Atomic = true

	if err := c.CopyTo(t.Context(), "/f", filepath.Join(local, "f")); err == nil {
		t.Fatal("CopyTo through failing writes: want an error")
	}
	if got := remoteNames(t, c, "/"); !reflect.DeepEqual(got, []string{"f"}) {
		t.Errorf("the remote holds %v, want nothing left behind", got)
	}
	back := t.TempDir()
	if err := c.CopyFrom(t.Context(), back, "/f"); err != nil {
		t.Fatal(err)
	}
	if got := readTree(t, back); got["f"] != "old" {
		t.Errorf("after a failure the target holds %q, want it as it was", got["f"])
	}

	t.Run("resumed", func(t *testing.T) {
		c.Resume = true
		if err := c.CopyTo(t.Context(), "/f", filepath.Join(local, "f")); err == nil {
			t.Fatal("CopyTo through failing writes: want an error")
		}
		if got := remoteNames(t, c, "/"); !reflect.DeepEqual(got, []string{".f.partial", "f"}) {
			t.Errorf("the remote holds %v, want the part kept beside the target", got)
		}
		f.failWritesFrom = -1
		if err := c.CopyTo(t.Context(), "/f", filepath.Join(local, "f")); err != nil {
			t.Fatalf("CopyTo: %v", err)
		}
		if got := remoteNames(t, c, "/"); !reflect.DeepEqual(got, []string{"f"}) {
			t.Errorf("the remote holds %v, want only the target", got)
		}
		back := t.TempDir()
		if err := c.CopyFrom(t.Context(), back, "/f"); err != nil {
			t.Fatal(err)
		}
		if got := readTree(t, back); got["f"] != string(content) {
			t.Errorf("the resumed file holds %d bytes, want the %d copied", len(got["f"]), len(content))
		}
	})
}
