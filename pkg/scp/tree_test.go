package scp

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// recordingSink notes every call, and fails the files named in failFiles.
type recordingSink struct {
	calls     []string
	failFiles map[string]bool
}

func (s *recordingSink) File(h Header, body io.Reader) error {
	data, _ := io.ReadAll(body)
	s.calls = append(s.calls, "file "+h.Name+"="+string(data))
	if s.failFiles[h.Name] {
		return errors.New("refused " + h.Name)
	}
	return nil
}
func (s *recordingSink) EnterDir(h Header) error {
	s.calls = append(s.calls, "enter "+h.Name)
	return nil
}
func (s *recordingSink) LeaveDir() error {
	s.calls = append(s.calls, "leave")
	return nil
}

func TestReceiveAllTakesFilesAndDirectories(t *testing.T) {
	in := strings.NewReader("C0644 1 a\nA\x00D0755 0 dir\nC0600 1 b\nB\x00D0700 0 sub\nC0644 0 c\n\x00E\nE\nC0644 1 d\nD\x00")
	var out bytes.Buffer
	sink := &recordingSink{}

	err := ReceiveAll(in, &out, ReceiveOptions{Recursive: true}, sink)

	if err != nil {
		t.Fatalf("ReceiveAll: %v", err)
	}
	want := []string{"file a=A", "enter dir", "file b=B", "enter sub", "file c=", "leave", "leave", "file d=D"}
	if strings.Join(sink.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %v, want %v", sink.calls, want)
	}
	if strings.Count(out.String(), "\x00") != 1+2*4+2+2 || strings.Contains(out.String(), "\x01") {
		t.Errorf("replies = %q", out.String())
	}
}

func TestReceiveAllCarriesOnPastAFailedFile(t *testing.T) {
	in := strings.NewReader("C0644 1 a\nA\x00C0644 1 b\nB\x00")
	var out bytes.Buffer
	sink := &recordingSink{failFiles: map[string]bool{"a": true}}

	err := ReceiveAll(in, &out, ReceiveOptions{}, sink)

	if err == nil || !strings.Contains(err.Error(), "refused a") {
		t.Fatalf("err = %v, want the failure of a", err)
	}
	if len(sink.calls) != 2 || sink.calls[1] != "file b=B" {
		t.Errorf("calls = %v, want b received after a failed", sink.calls)
	}
	if !strings.Contains(out.String(), "\x01refused a\n") {
		t.Errorf("replies = %q, want a told the sender", out.String())
	}
}

func TestReceiveAllSkipsAFileOverMaxSizeAndCarriesOn(t *testing.T) {
	// The sender skips the body of a refused file, so none follows its header.
	in := strings.NewReader("C0644 10 big\nC0644 1 small\nS\x00")
	var out bytes.Buffer
	sink := &recordingSink{}

	err := ReceiveAll(in, &out, ReceiveOptions{MaxSize: 5}, sink)

	if err == nil || len(sink.calls) != 1 || sink.calls[0] != "file small=S" {
		t.Fatalf("ReceiveAll = %v, calls %v", err, sink.calls)
	}
	if !strings.Contains(out.String(), "\x01big: file too large\n") {
		t.Errorf("replies = %q", out.String())
	}
}

func TestReceiveAllCollectsTheSendersOwnFailures(t *testing.T) {
	in := strings.NewReader("\x01scp: gone: No such file or directory\nC0644 1 a\nA\x00")
	sink := &recordingSink{}

	err := ReceiveAll(in, io.Discard, ReceiveOptions{}, sink)

	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Message != "scp: gone: No such file or directory" || len(sink.calls) != 1 {
		t.Fatalf("ReceiveAll = %v, calls %v", err, sink.calls)
	}
}

func TestReceiveAllStopsOnAFatalFailure(t *testing.T) {
	in := strings.NewReader("\x02scp: giving up\nC0644 1 a\nA\x00")
	sink := &recordingSink{}

	err := ReceiveAll(in, io.Discard, ReceiveOptions{}, sink)

	var remote *RemoteError
	if !errors.As(err, &remote) || !remote.Fatal || len(sink.calls) != 0 {
		t.Fatalf("ReceiveAll = %v, calls %v", err, sink.calls)
	}
}

func TestReceiveAllPassesTimesToDirectoriesAndFiles(t *testing.T) {
	in := strings.NewReader("T1700000000 0 1700000000 0\nD0755 0 dir\nT1700000100 0 1700000100 0\nC0644 0 f\n\x00E\n")
	var got []Header
	sink := &headerSink{seen: &got}

	if err := ReceiveAll(in, io.Discard, ReceiveOptions{Recursive: true}, sink); err != nil {
		t.Fatalf("ReceiveAll: %v", err)
	}
	if len(got) != 2 || got[0].ModTime.Unix() != 1700000000 || got[1].ModTime.Unix() != 1700000100 {
		t.Fatalf("headers = %+v", got)
	}
}

type headerSink struct{ seen *[]Header }

func (s *headerSink) File(h Header, _ io.Reader) error { *s.seen = append(*s.seen, h); return nil }
func (s *headerSink) EnterDir(h Header) error          { *s.seen = append(*s.seen, h); return nil }
func (s *headerSink) LeaveDir() error                  { return nil }

func TestReceiveAllRefusesWhatBreaksTheProtocol(t *testing.T) {
	cases := map[string]struct {
		in   string
		opts ReceiveOptions
	}{
		"directory without -r":    {"D0755 0 dir\nE\n", ReceiveOptions{}},
		"end without a directory": {"E\n", ReceiveOptions{Recursive: true}},
		"stopped inside a dir":    {"D0755 0 dir\n", ReceiveOptions{Recursive: true}},
		"malformed control line":  {"C0644 x a\n", ReceiveOptions{}},
		"malformed directory":     {"D0755 x dir\n", ReceiveOptions{Recursive: true}},
		"malformed time line":     {"T1 0\n", ReceiveOptions{}},
		"empty line":              {"\n", ReceiveOptions{}},
		"unknown line":            {"X\n", ReceiveOptions{}},
		"unterminated line":       {"C0644 1 a", ReceiveOptions{}},
		"name with a slash":       {"C0644 1 ../a\nA\x00", ReceiveOptions{}},
		"directory named ..":      {"D0755 0 ..\nE\n", ReceiveOptions{Recursive: true}},
		"body cut short":          {"C0644 5 a\nAB", ReceiveOptions{}},
		"no status after body":    {"C0644 1 a\nA", ReceiveOptions{}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ReceiveAll(strings.NewReader(c.in), io.Discard, c.opts, &recordingSink{}); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestSendFSSendsFilesAndDirectories(t *testing.T) {
	fsys := fstest.MapFS{
		"top.txt":       {Data: []byte("T"), Mode: 0o644},
		"dir":           {Mode: 0o755 | fs.ModeDir},
		"dir/a.txt":     {Data: []byte("A"), Mode: 0o600},
		"dir/sub":       {Mode: 0o755 | fs.ModeDir},
		"dir/sub/b.txt": {Data: []byte("BB"), Mode: 0o640},
	}
	acks := strings.Repeat("\x00", 32)
	var out bytes.Buffer

	err := SendFS(strings.NewReader(acks), &out, fsys, []string{"top.txt", "dir"}, SendOptions{Recursive: true})

	if err != nil {
		t.Fatalf("SendFS: %v", err)
	}
	want := "C0644 1 top.txt\nT\x00D0755 0 dir\nC0600 1 a.txt\nA\x00D0755 0 sub\nC0640 2 b.txt\nBB\x00E\nE\n"
	if out.String() != want {
		t.Errorf("wire = %q\nwant   %q", out.String(), want)
	}
}

func TestSendFSReportsAndSkipsWhatItCannotSend(t *testing.T) {
	fsys := fstest.MapFS{
		"dir":  {Mode: 0o755 | fs.ModeDir},
		"file": {Data: []byte("F"), Mode: 0o644},
	}
	var out bytes.Buffer

	err := SendFS(strings.NewReader(strings.Repeat("\x00", 8)), &out, fsys, []string{"missing", "dir", "file"}, SendOptions{})

	if err == nil || !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want both failures", err)
	}
	want := "\x01scp: missing: No such file or directory\n\x01scp: dir: not a regular file\nC0644 1 file\nF\x00"
	if out.String() != want {
		t.Errorf("wire = %q\nwant   %q", out.String(), want)
	}
}

func TestSendFSSkipsAFileTheReceiverRefuses(t *testing.T) {
	fsys := fstest.MapFS{"a": {Data: []byte("A")}, "b": {Data: []byte("B")}}
	// ready; a refused at its header; b accepted and stored.
	in := strings.NewReader("\x00\x01a: file too large\n\x00\x00")
	var out bytes.Buffer

	err := SendFS(in, &out, fsys, []string{"a", "b"}, SendOptions{})

	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Message != "a: file too large" {
		t.Fatalf("err = %v, want the refusal of a", err)
	}
	if out.String() != "C0000 1 a\nC0000 1 b\nB\x00" {
		t.Errorf("wire = %q, want no body for a", out.String())
	}
}

func TestSendFSSendsTimesWhenPreserving(t *testing.T) {
	mtime := time.Unix(1700000000, 0)
	fsys := fstest.MapFS{"a": {Data: []byte("A"), Mode: 0o644, ModTime: mtime}}
	var out bytes.Buffer

	if err := SendFS(strings.NewReader(strings.Repeat("\x00", 4)), &out, fsys, []string{"a"}, SendOptions{Preserve: true}); err != nil {
		t.Fatalf("SendFS: %v", err)
	}
	if out.String() != "T1700000000 0 1700000000 0\nC0644 1 a\nA\x00" {
		t.Errorf("wire = %q", out.String())
	}
}

func TestSendFSStopsWhenThePeerBreaksOff(t *testing.T) {
	fsys := fstest.MapFS{"a": {Data: []byte("A")}}

	err := SendFS(strings.NewReader("\x00"), io.Discard, fsys, []string{"a"}, SendOptions{})

	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("err = %v, want ErrProtocol", err)
	}
}
