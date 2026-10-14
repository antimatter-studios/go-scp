package scp

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var (
	atime = time.Unix(1_610_000_000, 0) // 2021
	mtime = time.Unix(1_580_000_000, 0) // 2020
)

func TestPreserveSendsTheAccessTime(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "f")
	if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, atime, mtime); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	if err := SendFS(strings.NewReader(strings.Repeat("\x00", 4)), &out, os.DirFS(dir), []string{"f"}, SendOptions{Preserve: true}); err != nil {
		t.Fatalf("SendFS: %v", err)
	}
	want := fmt.Sprintf("T%d 0 %d 0\n", mtime.Unix(), atime.Unix())
	if !strings.HasPrefix(out.String(), want) {
		t.Errorf("wire = %q, want it to start %q", out.String(), want)
	}
}

func TestAccessTimeFallsBackToTheModificationTime(t *testing.T) {
	info, err := fs.Stat(fstest.MapFS{"f": {ModTime: mtime}}, "f")
	if err != nil {
		t.Fatal(err)
	}
	if got := AccessTime(info); !got.Equal(mtime) {
		t.Errorf("AccessTime = %v, want the modification time %v", got, mtime)
	}
}
