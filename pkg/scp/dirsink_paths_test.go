package scp

import (
	"io"
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

func TestDirSinkKeepsOnlyTheBytesThatArrived(t *testing.T) {
	dir := t.TempDir()
	// An existing longer file, to check it is cut down rather than kept or padded.
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("an older and longer file"), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := newDirSink(t, dir, DirSinkOptions{})

	err := sink.File(Header{Mode: 0o644, Size: 1 << 30, Name: "a"}, strings.NewReader("short"))

	if err == nil || !strings.Contains(err.Error(), "5 of 1073741824 bytes") {
		t.Errorf("File = %v, want the short body reported", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "a")); string(got) != "short" {
		t.Errorf("file holds %q, want only the bytes that arrived", got)
	}
}

func TestDirSinkWritesNoMoreThanTheDeclaredSize(t *testing.T) {
	dir := t.TempDir()
	sink := newDirSink(t, dir, DirSinkOptions{})

	if err := sink.File(Header{Mode: 0o644, Size: 2, Name: "a"}, strings.NewReader("abcdef")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "a")); string(got) != "ab" {
		t.Errorf("file holds %q, want the declared 2 bytes", got)
	}
}

func TestReceiveAllLeavesNoPaddedFileWhenTheSenderStops(t *testing.T) {
	dir := t.TempDir()
	sink := newDirSink(t, dir, DirSinkOptions{})

	err := ReceiveAll(strings.NewReader("C0644 1000000 big\nonly this"), io.Discard, ReceiveOptions{}, sink)

	if err == nil {
		t.Fatal("ReceiveAll: want an error for the short body")
	}
	if info, statErr := os.Stat(filepath.Join(dir, "big")); statErr != nil || info.Size() != int64(len("only this")) {
		t.Errorf("big: %v, size %d; want only the 9 bytes that arrived", statErr, info.Size())
	}
}

func TestDirSinkCannotEnterADirectoryItCannotOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens any directory")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	sink := newDirSink(t, dir, DirSinkOptions{})

	if err := sink.EnterDir(Header{Mode: 0o755, Name: "locked"}); err == nil {
		t.Error("entering a directory that cannot be opened: want an error")
	}
}

func TestDirSinkCloseReleasesDirectoriesStillOpen(t *testing.T) {
	sink, err := NewDirSink(t.TempDir(), DirSinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.EnterDir(Header{Mode: 0o755, Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := sink.EnterDir(Header{Mode: 0o755, Name: "b"}); err != nil {
		t.Fatal(err)
	}

	if err := sink.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if len(sink.dirs) != 0 {
		t.Errorf("%d directories still held", len(sink.dirs))
	}
}

func TestDirSinkResumesAFileFromWhereItEnds(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("AB"), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := newDirSink(t, dir, DirSinkOptions{Resume: true})
	h := Header{Mode: 0o644, Size: 10, Name: "f"}

	offset, err := sink.ResumeFrom(h)
	if err != nil || offset != 2 {
		t.Fatalf("ResumeFrom = (%d, %v), want 2", offset, err)
	}
	if err := sink.File(h, strings.NewReader("23456789")); err != nil {
		t.Fatalf("File: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "f")); string(got) != "AB23456789" {
		t.Errorf("file holds %q", got)
	}
	if err := sink.File(Header{Mode: 0o644, Size: 10, Name: "g"}, strings.NewReader("abc")); err == nil || !strings.Contains(err.Error(), "3 of 10 bytes") {
		t.Errorf("a short body when resuming: %v, want it reported", err)
	}
}

func TestDirSinkResumeFromReportsWhatItCannotResume(t *testing.T) {
	sink := newDirSink(t, filepath.Join(t.TempDir(), "new"), DirSinkOptions{Resume: true})
	if err := sink.File(Header{Mode: 0o644, Name: "a"}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.ResumeFrom(Header{Name: "b"}); err == nil {
		t.Error("a second entry for a target that is not a directory: want an error")
	}

	dir := t.TempDir()
	sink = newDirSink(t, dir, DirSinkOptions{Resume: true})
	if _, err := sink.ResumeFrom(Header{Name: strings.Repeat("n", 300)}); err == nil {
		t.Error("a name the system refuses: want an error")
	}
	if err := os.WriteFile(filepath.Join(dir, "long"), []byte("longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sink.File(Header{Mode: 0o644, Size: 2, Name: "long"}, strings.NewReader("ab")); err == nil {
		t.Error("resuming over a longer file: want an error")
	}
}
