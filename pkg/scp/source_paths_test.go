package scp

import (
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

// faultyFS is a MapFS whose chosen operations fail. Stat still answers from the
// map, so an entry is found and then fails to open, read or list.
type faultyFS struct {
	fstest.MapFS
	openErr, readErr, readDirErr error
}

func (f faultyFS) Open(name string) (fs.File, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	file, err := f.MapFS.Open(name)
	if err != nil || f.readErr == nil {
		return file, err
	}
	return faultyFile{file, f.readErr}, nil
}

func (f faultyFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if f.readDirErr != nil {
		return nil, f.readDirErr
	}
	return f.MapFS.ReadDir(name)
}

type faultyFile struct {
	fs.File
	err error
}

func (f faultyFile) Read([]byte) (int, error) { return 0, f.err }

// Each case is one way sending an entry can go wrong. in is what the receiver
// says: a status byte per step, then a message line after a failure.
func TestSendFSPaths(t *testing.T) {
	file := fstest.MapFS{"f": {Data: []byte("x"), Mode: 0o644, ModTime: modTime}}
	dir := fstest.MapFS{"d": {Mode: fs.ModeDir | 0o755, ModTime: modTime}}
	dirWithFile := fstest.MapFS{"d/f": {Data: []byte("x"), Mode: 0o644}}
	plain := SendOptions{}
	timed := SendOptions{Preserve: true}
	tree := SendOptions{Recursive: true}
	timedTree := SendOptions{Recursive: true, Preserve: true}

	cases := map[string]struct {
		fsys fs.FS
		name string
		opts SendOptions
		in   string
		out  io.Writer
		want string
	}{
		"name the receiver cannot take":              {fstest.MapFS{"a\nb": {}}, "a\nb", plain, "\x00", io.Discard, "a\nb"},
		"report cannot be sent":                      {file, "missing", plain, "\x00", &failingStdin{}, "scripted failure"},
		"file cannot be opened":                      {faultyFS{MapFS: file, openErr: fs.ErrPermission}, "f", plain, "\x00", io.Discard, "permission denied"},
		"file times cannot be sent":                  {file, "f", timed, "\x00", &failingStdin{}, "scripted failure"},
		"file times refused":                         {file, "f", timed, "\x00\x01no times\n", io.Discard, "no times"},
		"file times end the transfer":                {file, "f", timed, "\x00\x02gone\n", io.Discard, "gone"},
		"header cannot be sent":                      {file, "f", plain, "\x00", &failingStdin{}, "scripted failure"},
		"header ends the transfer":                   {file, "f", plain, "\x00\x02gone\n", io.Discard, "gone"},
		"file read fails":                            {faultyFS{MapFS: file, readErr: errScripted}, "f", plain, "\x00\x00\x00", io.Discard, "scp: f: scripted failure"},
		"padding cannot be sent":                     {faultyFS{MapFS: file, readErr: errScripted}, "f", plain, "\x00\x00", &failingStdin{ok: 1}, "scripted failure"},
		"read failure cannot be sent":                {faultyFS{MapFS: file, readErr: errScripted}, "f", plain, "\x00\x00", &failingStdin{ok: 2}, "scripted failure"},
		"end of body cannot be sent":                 {file, "f", plain, "\x00\x00", &failingStdin{ok: 2}, "scripted failure"},
		"receiver ends after the body":               {file, "f", plain, "\x00\x00\x02gone\n", io.Discard, "gone"},
		"receiver cannot store after a read failure": {faultyFS{MapFS: file, readErr: errScripted}, "f", plain, "\x00\x00\x01full\n", io.Discard, "scp: f: scripted failure"},
		"directory cannot be listed":                 {faultyFS{MapFS: dir, readDirErr: fs.ErrPermission}, "d", tree, "\x00", io.Discard, "permission denied"},
		"directory times refused":                    {dir, "d", timedTree, "\x00\x01no times\n", io.Discard, "no times"},
		"directory times end the transfer":           {dir, "d", timedTree, "\x00\x02gone\n", io.Discard, "gone"},
		"directory line cannot be sent":              {dir, "d", tree, "\x00", &failingStdin{}, "scripted failure"},
		"directory refused":                          {dir, "d", tree, "\x00\x01no dirs\n", io.Discard, "no dirs"},
		"directory ends the transfer":                {dir, "d", tree, "\x00\x02gone\n", io.Discard, "gone"},
		"entry inside ends the transfer":             {dirWithFile, "d", tree, "\x00\x00", &failingStdin{ok: 1}, "scripted failure"},
		"entry inside refused":                       {dirWithFile, "d", tree, "\x00\x00\x01no files\n\x00", io.Discard, "no files"},
		"end of directory cannot be sent":            {dir, "d", tree, "\x00\x00", &failingStdin{ok: 1}, "scripted failure"},
		"end of directory not answered":              {dir, "d", tree, "\x00\x00", io.Discard, "expected a status byte"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := SendFS(strings.NewReader(c.in), c.out, c.fsys, []string{c.name}, c.opts)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("SendFS = %v, want an error containing %q", err, c.want)
			}
		})
	}
}
