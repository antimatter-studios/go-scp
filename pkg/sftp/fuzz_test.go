package sftp

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pkgsftp "github.com/pkg/sftp"
)

// listedServer serves one directory, /d, whose entries are whatever names it is
// given, each a file holding its own name as content. It is how a fuzz target
// hands the client a listing it chose.
type listedServer struct {
	files map[string][]byte // by path, as the client asks for them
	names []string          // as the listing gives them
}

func (s *listedServer) Fileread(r *pkgsftp.Request) (io.ReaderAt, error) {
	content, ok := s.files[r.Filepath]
	if !ok {
		return nil, os.ErrNotExist
	}
	return bytes.NewReader(content), nil
}

func (s *listedServer) Filewrite(*pkgsftp.Request) (io.WriterAt, error) {
	return nil, os.ErrPermission
}

func (s *listedServer) Filecmd(*pkgsftp.Request) error { return os.ErrPermission }

func (s *listedServer) Filelist(r *pkgsftp.Request) (pkgsftp.ListerAt, error) {
	if r.Method == "List" {
		if r.Filepath != "/d" {
			return nil, os.ErrNotExist
		}
		entries := make(listing, len(s.names))
		for i, n := range s.names {
			entries[i] = fileEntry{name: n, size: int64(len(n))}
		}
		return entries, nil
	}
	if r.Filepath == "/d" {
		return listing{fileEntry{name: "d", dir: true}}, nil
	}
	content, ok := s.files[r.Filepath]
	if !ok {
		return nil, os.ErrNotExist
	}
	return listing{fileEntry{name: path.Base(r.Filepath), size: int64(len(content))}}, nil
}

// fileEntry is a file or directory with a name and a size, as a listing gives it.
type fileEntry struct {
	name string
	size int64
	dir  bool
}

func (e fileEntry) Name() string { return e.name }
func (e fileEntry) Size() int64  { return e.size }
func (e fileEntry) Mode() os.FileMode {
	if e.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (e fileEntry) ModTime() time.Time { return modTime }
func (e fileEntry) IsDir() bool        { return e.dir }
func (e fileEntry) Sys() any           { return nil }

// A server that lists any names at all, each a file, must not lead CopyFrom to
// write outside its target, nor to write more than the server sent.
func FuzzCopyFromListing(f *testing.F) {
	for _, seed := range []string{"a\nb", "../escape\nok", ".\n..\n", "a/b\nc", "\x00\nsame\nsame", strings.Repeat("x", 300)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, names string) {
		server := &listedServer{files: map[string][]byte{}, names: strings.Split(names, "\n")}
		var sent int64
		for _, n := range server.names {
			p := path.Join("/d", n)
			server.files[p] = []byte(n)
		}
		for _, content := range server.files {
			sent += int64(len(content))
		}
		c := &Client{Recursive: true, session: pipeSession(pkgsftp.Handlers{FileGet: server, FilePut: server, FileCmd: server, FileList: server})}
		base := t.TempDir()
		target := filepath.Join(base, "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}

		c.CopyFrom(t.Context(), target, "/d")

		if entries, err := os.ReadDir(base); err != nil || len(entries) != 1 {
			t.Fatalf("the target's directory holds %v (%v), want only the target", entries, err)
		}
		var written int64
		filepath.WalkDir(target, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				if info, err := d.Info(); err == nil {
					written += info.Size()
				}
			}
			return nil
		})
		if written > sent {
			t.Fatalf("%d bytes written from a server that sent %d", written, sent)
		}
	})
}

// A leading ~ is the only thing remotePath changes, and it never gives an empty
// path.
func FuzzRemotePath(f *testing.F) {
	for _, seed := range []string{"~", "~/", "~//x", "~/~/x", "~user/x", "/abs", "", "rel"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, p string) {
		got := remotePath(p)
		if !strings.HasPrefix(p, "~") && got != p {
			t.Fatalf("remotePath(%q) = %q, want it unchanged", p, got)
		}
		if p != "" && got == "" {
			t.Fatalf("remotePath(%q) is empty", p)
		}
	})
}
