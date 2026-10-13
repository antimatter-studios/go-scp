package sftp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkgsftp "github.com/pkg/sftp"
)

// Each destination's partial file differs from its source in the part it already
// holds, so the result shows that only the rest was copied.
func TestResumeContinuesPartialCopies(t *testing.T) {
	local, remote, back := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"up": "0123456789"})
	writeTree(t, remote, map[string]string{"up": "XYZ", "down": "0123456789"})
	writeTree(t, back, map[string]string{"down": "AB"})
	c := programClient(t, remote)
	c.Resume = true

	if err := c.CopyTo(t.Context(), remote, filepath.Join(local, "up")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(remote, "up")); string(got) != "XYZ3456789" {
		t.Errorf("uploaded file holds %q, want the rest after what was there", got)
	}
	if err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "down")); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(back, "down")); string(got) != "AB23456789" {
		t.Errorf("downloaded file holds %q, want the rest after what was here", got)
	}
}

func TestResumeLeavesACompleteFileAlone(t *testing.T) {
	remote, back := t.TempDir(), t.TempDir()
	writeTree(t, remote, map[string]string{"f": "0123456789"})
	writeTree(t, back, map[string]string{"f": "ABCDEFGHIJ"})
	c := programClient(t, remote)
	c.Resume = true

	if err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "f")); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(back, "f")); string(got) != "ABCDEFGHIJ" {
		t.Errorf("file holds %q, want it left as it was", got)
	}
}

func TestResumeRefusesADestinationLongerThanItsSource(t *testing.T) {
	local, remote, back := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"f": "short"})
	writeTree(t, remote, map[string]string{"f": "much longer than the source", "g": "short"})
	writeTree(t, back, map[string]string{"g": "much longer than the source"})
	c := programClient(t, remote)
	c.Resume = true

	if err := c.CopyTo(t.Context(), remote, filepath.Join(local, "f")); err == nil || !strings.Contains(err.Error(), "not resuming") {
		t.Errorf("CopyTo = %v, want the longer destination refused", err)
	}
	if err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "g")); err == nil || !strings.Contains(err.Error(), "not resuming") {
		t.Errorf("CopyFrom = %v, want the longer destination refused", err)
	}
}

func TestResumeReportsADestinationItCannotLookAt(t *testing.T) {
	local := t.TempDir()
	writeTree(t, local, map[string]string{"f": "x"})
	f := newFaults()
	f.fail = func(r *pkgsftp.Request) bool { return r.Method == "Stat" && r.Filepath == "/f" }
	c := faultyClient(f)
	c.Resume = true

	if err := c.CopyTo(t.Context(), "/f", filepath.Join(local, "f")); err == nil || !strings.Contains(err.Error(), "chosen failure") {
		t.Errorf("CopyTo = %v, want the failed look reported", err)
	}
}
