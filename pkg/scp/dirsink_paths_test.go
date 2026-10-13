package scp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func newDirSink(t *testing.T, target string, opts DirSinkOptions) *DirSink {
	t.Helper()
	sink, err := NewDirSink(target, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sink.Close() })
	return sink
}

func TestDirSinkFileFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	sink := newDirSink(t, dir, DirSinkOptions{})

	if err := sink.File(Header{Mode: 0o644, Name: "taken"}, strings.NewReader("")); err == nil {
		t.Error("writing a file over a directory: want an error")
	}
	if err := sink.File(Header{Mode: 0o644, Size: 1, Name: "a"}, iotest.ErrReader(errScripted)); err == nil {
		t.Error("a body that cannot be read: want an error")
	}
	if err := sink.File(Header{Mode: 0o644, Size: -1, Name: "b"}, strings.NewReader("")); err == nil {
		t.Error("a negative size: want an error")
	}
}

func TestDirSinkPreservesWhateverTimesArrive(t *testing.T) {
	dir := t.TempDir()
	sink := newDirSink(t, dir, DirSinkOptions{Preserve: true})
	at := time.Unix(1_600_000_000, 0)

	if err := sink.File(Header{Mode: 0o600, Name: "none"}, strings.NewReader("")); err != nil {
		t.Fatalf("no times: %v", err)
	}
	if err := sink.File(Header{Mode: 0o600, Name: "access", AccessTime: at}, strings.NewReader("")); err != nil {
		t.Fatalf("an access time only: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "access"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(at) {
		t.Errorf("modification time = %v, want the access time %v", info.ModTime(), at)
	}
}

func TestDirSinkTargetTakesOneEntry(t *testing.T) {
	sink := newDirSink(t, filepath.Join(t.TempDir(), "new"), DirSinkOptions{})

	if err := sink.File(Header{Mode: 0o644, Name: "a"}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if err := sink.EnterDir(Header{Mode: 0o755, Name: "d"}); err == nil {
		t.Error("a second entry for a target that is not a directory: want an error")
	}
}

func TestDirSinkCannotCreateADirectoryOverALink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("missing", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	sink := newDirSink(t, dir, DirSinkOptions{})

	if err := sink.EnterDir(Header{Mode: 0o755, Name: "link"}); err == nil {
		t.Error("a directory where a link points nowhere: want an error")
	}
}

func TestDirSinkLeavingARemovedDirectoryFails(t *testing.T) {
	dir := t.TempDir()
	sink := newDirSink(t, dir, DirSinkOptions{Preserve: true})
	if err := sink.EnterDir(Header{Mode: 0o755, Name: "d"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "d")); err != nil {
		t.Fatal(err)
	}

	if err := sink.LeaveDir(); err == nil {
		t.Error("leaving a directory that is gone: want an error")
	}
}
