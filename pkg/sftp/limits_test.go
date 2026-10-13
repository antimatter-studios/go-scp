package sftp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLimitPacesSFTPCopies(t *testing.T) {
	local, back := t.TempDir(), t.TempDir()
	// 40,000 bytes at 100,000 a second is 0.4 seconds each way.
	writeTree(t, local, map[string]string{"f": strings.Repeat("x", 40_000)})
	c := faultyClient(newFaults())
	c.Limit = 100_000

	start := time.Now()
	if err := c.CopyTo(t.Context(), "/f", filepath.Join(local, "f")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if up := time.Since(start); up < 300*time.Millisecond {
		t.Errorf("upload took %v, want the limit to hold it to about 0.4s", up)
	}
	start = time.Now()
	if err := c.CopyFrom(t.Context(), back, "/f"); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if down := time.Since(start); down < 300*time.Millisecond {
		t.Errorf("download took %v, want the limit to hold it to about 0.4s", down)
	}
	if got := readTree(t, back); got["f"] != strings.Repeat("x", 40_000) {
		t.Errorf("copied back %d bytes, want 40000", len(got["f"]))
	}
}

func TestAPacerMovesAtLeastAByteAtATime(t *testing.T) {
	if got := newPacer(5).chunk(); got != 1 {
		t.Errorf("chunk at 5 bytes a second = %d, want 1", got)
	}
	if newPacer(0) != nil {
		t.Error("a pacer for no limit: want none")
	}
}

func TestMaxSizeRefusesLargerSFTPDownloads(t *testing.T) {
	remote, back := t.TempDir(), t.TempDir()
	writeTree(t, remote, map[string]string{"big": strings.Repeat("b", 100), "small": "s"})
	c := programClient(t, remote)
	c.MaxSize = 10

	err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "big"), filepath.Join(remote, "small"))

	if err == nil || !strings.Contains(err.Error(), "more than the 10") {
		t.Errorf("CopyFrom = %v, want the large file refused", err)
	}
	if got := readTree(t, back); !reflect.DeepEqual(got, map[string]string{"small": "s"}) {
		t.Errorf("copied %v, want only the small file", got)
	}
	if _, err := os.Stat(filepath.Join(back, "big")); !os.IsNotExist(err) {
		t.Errorf("big: %v, want nothing written for it", err)
	}
}
