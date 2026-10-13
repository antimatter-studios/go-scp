package scp

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"
)

// Fuzz targets for every part that reads what the other end sends. Each one runs
// its seeds as an ordinary test; `go test -fuzz FuzzName ./pkg/scp` explores from
// them. What each checks beyond "does not panic" is written next to it.

// A receiving end fed any byte stream must finish, never write outside its target,
// and never write more bytes than arrived.
func FuzzReceiveAll(f *testing.F) {
	for _, seed := range []string{
		"C0644 1 a\nA\x00",
		"D0755 0 dir\nC0600 1 b\nB\x00E\n",
		"T1 0 1 0\nC0644 2 t\nhi\x00",
		"\x01scp: x: No such file or directory\nC0644 1 y\nY\x00",
		"C0644 9 big\nshort",
		"D0755 0 ..\nE\n",
		"C0644 1 a/b\nX\x00",
		"D0400 0 d\nC0644 1 f\nX\x00E\n", // a directory that can be listed but not entered
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, stream []byte) {
		base := t.TempDir()
		target := filepath.Join(base, "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		sink, err := NewDirSink(target, DirSinkOptions{Preserve: true})
		if err != nil {
			t.Fatal(err)
		}
		defer sink.Close()

		ReceiveAll(bytes.NewReader(stream), io.Discard, ReceiveOptions{Recursive: true, MaxSize: 1 << 16}, sink)

		entries, err := os.ReadDir(base)
		if err != nil || len(entries) != 1 {
			t.Fatalf("the target's directory holds %v (%v), want only the target", entries, err)
		}
		// With -p a sender may ask for directories that cannot be listed or
		// entered; open each one before the walk goes into it, so every file is
		// counted and the test can clean up after itself.
		var written int64
		filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
			case d.IsDir():
				os.Chmod(p, 0o700)
			case d.Type().IsRegular():
				if info, err := d.Info(); err == nil {
					written += info.Size()
				}
			}
			return nil
		})
		if written > int64(len(stream)) {
			t.Fatalf("%d bytes written from a stream of %d", written, len(stream))
		}
	})
}

// A download fed any reply must finish, and on success return exactly the bytes its
// header declared.
func FuzzDownload(f *testing.F) {
	for _, seed := range []string{
		"C0644 1 x\nX\x00",
		"T1 0 1 0\nC0644 1 x\nX\x00",
		"\x01scp: /x: No such file or directory\n",
		"C0644 5 x\nAB",
		"D0755 0 d\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, reply string) {
		c := scripted(&scriptedSession{stdout: reply})
		c.MaxSize = 1 << 16
		var got bytes.Buffer
		h, err := c.Download(t.Context(), "/x", &got)
		if err == nil && int64(got.Len()) != h.Size {
			t.Fatalf("returned %d bytes for a header of %d", got.Len(), h.Size)
		}
	})
}

// A sending end fed any answers must finish.
func FuzzSendFS(f *testing.F) {
	fsys := fstest.MapFS{
		"top.txt":     {Data: []byte("top"), Mode: 0o644, ModTime: modTime},
		"dir/a.txt":   {Data: []byte("a"), Mode: 0o600, ModTime: modTime},
		"dir/sub/b":   {Data: []byte("b"), Mode: 0o644, ModTime: modTime},
		"dir/sub/c\n": {Data: []byte("c"), Mode: 0o644},
	}
	for _, seed := range []string{
		strings.Repeat("\x00", 20),
		"\x00\x01no\n" + strings.Repeat("\x00", 20),
		"\x00\x02gone\n",
		"",
		"\x05",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, answers string) {
		SendFS(strings.NewReader(answers), io.Discard, fsys, []string{"top.txt", "dir", "missing"}, SendOptions{Recursive: true, Preserve: true})
	})
}

// A path the client sends reaches a server's ParseCommand unchanged, whatever it
// holds, in both directions.
func FuzzCommandRoundTrip(f *testing.F) {
	for _, seed := range []string{"notes.txt", "/srv/it's here", "", "-r", "--", "a b\tc\nd", `\`, `"`, "$HOME", "*.txt"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, p string) {
		c := &Client{}
		for _, flags := range []string{"-t", "-f"} {
			cmd := c.command(flags, Quote(p))
			got, err := ParseCommand(cmd)
			if err != nil {
				t.Fatalf("ParseCommand(%q): %v", cmd, err)
			}
			if len(got.Paths) != 1 || got.Paths[0] != p || got.Patterns[0] {
				t.Fatalf("ParseCommand(%q) = %q %v, want the path %q back", cmd, got.Paths, got.Patterns, p)
			}
		}
	})
}

// Any command line either fails to parse or gives paths that stay inside the root
// once made relative: valid in an fs.FS wherever they are valid UTF-8.
func FuzzParseCommand(f *testing.F) {
	for _, seed := range []string{
		"scp -t /tmp/x",
		"scp -r -p -f -- 'it'\\''s' \"dq\" ../../etc/passwd",
		"scp -f /srv/*.txt",
		"scp -t",
		"/usr/bin/scp -f a\\ b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		c, err := ParseCommand(cmd)
		if err != nil {
			return
		}
		for _, p := range c.Relative().Paths {
			if utf8.ValidString(p) && !fs.ValidPath(p) {
				t.Fatalf("%q gives %q, which is not a valid path under the root", cmd, p)
			}
		}
	})
}

// A time line either fails to parse or reads back as the same times.
func FuzzParseTimes(f *testing.F) {
	for _, seed := range []string{"T1 0 2 0", "T1700000000 999999 1700000000 0", "T-1 0 0 0", "T1 1000000 0 0", "T9223372036854775807 0 0 0", "T"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		var h Header
		if err := parseTimes(line, &h); err != nil {
			return
		}
		var again Header
		if err := parseTimes(h.timesLine(), &again); err != nil || !again.ModTime.Equal(h.ModTime) || !again.AccessTime.Equal(h.AccessTime) {
			t.Fatalf("%q read as %v %v, which does not round-trip through %q: %v", line, h.ModTime, h.AccessTime, h.timesLine(), err)
		}
	})
}
