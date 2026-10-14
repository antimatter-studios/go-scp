package sftp

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/antimatter-studios/go-scp/pkg/scp"
	pkgsftp "github.com/pkg/sftp"
)

var errFault = errors.New("chosen failure")

// faults serves an in-memory filesystem over SFTP and fails the requests a test
// picks: those fail returns true for, writes at or past failWritesFrom once it is
// set, and listings of a directory named in listing, which answer with the names
// given there.
type faults struct {
	mu             sync.Mutex
	fail           func(*pkgsftp.Request) bool
	failWritesFrom int64 // -1 for none
	listing        map[string][]string
	mem            pkgsftp.Handlers

	// latency delays every read, as a slow link would delay each request's
	// answer; reading counts the reads waiting at once, and mostReading the most
	// there have been.
	latency     time.Duration
	reading     atomic.Int32
	mostReading atomic.Int32
}

func (f *faults) fails(r *pkgsftp.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail != nil && f.fail(r)
}

func (f *faults) set(fail func(*pkgsftp.Request) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

func (f *faults) Fileread(r *pkgsftp.Request) (io.ReaderAt, error) {
	if f.fails(r) {
		return nil, errFault
	}
	reader, err := f.mem.FileGet.Fileread(r)
	if err != nil || f.latency == 0 {
		return reader, err
	}
	return slowReaderAt{reader, f}, nil
}

// slowReaderAt answers each read after its server's latency, counting the reads
// that wait at once.
type slowReaderAt struct {
	io.ReaderAt
	f *faults
}

func (s slowReaderAt) ReadAt(p []byte, off int64) (int, error) {
	now := s.f.reading.Add(1)
	for most := s.f.mostReading.Load(); now > most && !s.f.mostReading.CompareAndSwap(most, now); most = s.f.mostReading.Load() {
	}
	time.Sleep(s.f.latency)
	s.f.reading.Add(-1)
	return s.ReaderAt.ReadAt(p, off)
}

func (f *faults) Filewrite(r *pkgsftp.Request) (io.WriterAt, error) {
	if f.fails(r) {
		return nil, errFault
	}
	w, err := f.mem.FilePut.Filewrite(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil && f.failWritesFrom >= 0 {
		return failingWriterAt{w, f.failWritesFrom}, nil
	}
	return w, err
}

func (f *faults) Filecmd(r *pkgsftp.Request) error {
	if f.fails(r) {
		return errFault
	}
	// The in-memory filesystem has no modes or times to set, and sets attributes
	// by opening the path as a file, which fails on a directory.
	if r.Method == "Setstat" {
		return nil
	}
	return f.mem.FileCmd.Filecmd(r)
}

// PosixRename replaces the target, as posix-rename@openssh.com does. Without it
// the request server would take the extension for a plain Rename, which refuses
// an existing target.
func (f *faults) PosixRename(r *pkgsftp.Request) error {
	if f.fails(r) {
		return errFault
	}
	return f.mem.FileCmd.(pkgsftp.PosixRenameFileCmder).PosixRename(r)
}

func (f *faults) Filelist(r *pkgsftp.Request) (pkgsftp.ListerAt, error) {
	if f.fails(r) {
		return nil, errFault
	}
	f.mu.Lock()
	names, ok := f.listing[r.Filepath]
	f.mu.Unlock()
	if ok && r.Method == "List" {
		infos := make(listing, len(names))
		for i, n := range names {
			infos[i] = namedFile(n)
		}
		return infos, nil
	}
	return f.mem.FileList.Filelist(r)
}

// failingWriterAt fails every write that reaches from or past it.
type failingWriterAt struct {
	io.WriterAt
	from int64
}

func (w failingWriterAt) WriteAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > w.from {
		return 0, errFault
	}
	return w.WriterAt.WriteAt(p, off)
}

type listing []os.FileInfo

func (l listing) ListAt(out []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[offset:])
	if int(offset)+n == len(l) {
		return n, io.EOF
	}
	return n, nil
}

// namedFile is a file with nothing but a name, as a listing may give.
type namedFile string

func (n namedFile) Name() string     { return string(n) }
func (namedFile) Size() int64        { return 0 }
func (namedFile) Mode() os.FileMode  { return 0o644 }
func (namedFile) ModTime() time.Time { return modTime }
func (namedFile) IsDir() bool        { return false }
func (namedFile) Sys() any           { return nil }

// faultyClient returns a Client whose sessions reach f's in-memory filesystem.
func faultyClient(f *faults) *Client {
	return &Client{session: pipeSession(pkgsftp.Handlers{FileGet: f, FilePut: f, FileCmd: f, FileList: f})}
}

// pipeSession starts sessions with an in-memory SFTP server that answers with h.
func pipeSession(h pkgsftp.Handlers) func(...pkgsftp.ClientOption) (*pkgsftp.Client, func() error, error) {
	return func(opts ...pkgsftp.ClientOption) (*pkgsftp.Client, func() error, error) {
		clientReads, serverWrites := io.Pipe()
		serverReads, clientWrites := io.Pipe()
		server := pkgsftp.NewRequestServer(stdio{serverReads, serverWrites}, h)
		go server.Serve()
		c, err := pkgsftp.NewClientPipe(clientReads, clientWrites, opts...)
		if err != nil {
			server.Close()
			return nil, nil, err
		}
		// The server goes first: closing it ends the pipe the client reads, which
		// the client's Close waits for.
		return c, func() error {
			err := server.Close()
			c.Close()
			return err
		}, nil
	}
}

func newFaults() *faults { return &faults{mem: pkgsftp.InMemHandler(), failWritesFrom: -1} }

// method picks requests by method and path.
func method(m, path string) func(*pkgsftp.Request) bool {
	return func(r *pkgsftp.Request) bool { return r.Method == m && r.Filepath == path }
}

// setstat picks a set-attributes request on path that sets the mode, or the times.
func setstat(path string, times bool) func(*pkgsftp.Request) bool {
	return func(r *pkgsftp.Request) bool {
		flags := r.AttrFlags()
		return r.Method == "Setstat" && r.Filepath == path && (times && flags.Acmodtime || !times && flags.Permissions)
	}
}

func TestCopyToReportsEachStepTheServerFails(t *testing.T) {
	local := t.TempDir()
	writeTree(t, local, map[string]string{"f": "x", "big": strings.Repeat("b", 40000)})
	empty := filepath.Join(local, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		fail       func(*pkgsftp.Request) bool
		failWrites bool
		from       int64
		preserve   bool
		source     string
	}{
		"file cannot be created":        {fail: method("Put", "/f"), source: "f"},
		"new file's mode cannot be set": {fail: setstat("/f", false), source: "f"},
		"file cannot be written":        {failWrites: true, source: "f"},
		// The last part ends the file part-way through a packet, where
		// github.com/pkg/sftp's File.ReadFrom would drop the error.
		"last part cannot be written":        {failWrites: true, from: 32768, source: "big"},
		"file's times cannot be set":         {fail: setstat("/f", true), preserve: true, source: "f"},
		"directory cannot be created":        {fail: method("Mkdir", "/empty"), source: "empty"},
		"new directory's mode cannot be set": {fail: setstat("/empty", false), source: "empty"},
		"directory's times cannot be set":    {fail: setstat("/empty", true), preserve: true, source: "empty"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFaults()
			f.fail = c.fail
			if c.failWrites {
				f.failWritesFrom = c.from
			}
			client := faultyClient(f)
			client.Recursive, client.Preserve = true, c.preserve
			if err := client.CopyTo(t.Context(), "/"+c.source, filepath.Join(local, c.source)); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestCopyFromReportsEachStepTheServerFails(t *testing.T) {
	cases := map[string]struct {
		fail    func(*pkgsftp.Request) bool
		listing map[string][]string
		want    string
	}{
		"file cannot be opened":      {fail: method("Get", "/d/f"), want: "chosen failure"},
		"directory cannot be listed": {fail: method("List", "/d"), want: "chosen failure"},
		"listing holds a path":       {listing: map[string][]string{"/d": {"../escape", "", "."}}, want: "the server listed"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFaults()
			local := t.TempDir()
			writeTree(t, local, map[string]string{"d/f": "x"})
			client := faultyClient(f)
			client.Recursive = true
			if err := client.CopyTo(t.Context(), "/d", filepath.Join(local, "d")); err != nil {
				t.Fatalf("setting up: %v", err)
			}
			f.set(c.fail)
			f.listing = c.listing

			err := client.CopyFrom(t.Context(), t.TempDir(), "/d")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("CopyFrom = %v, want %q", err, c.want)
			}
		})
	}
}

func TestCancellingDuringACopyReturnsTheContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := newFaults()
	f.fail = func(r *pkgsftp.Request) bool {
		cancel()
		return true
	}

	if err := faultyClient(f).CopyFrom(ctx, t.TempDir(), "/x"); !errors.Is(err, context.Canceled) {
		t.Errorf("CopyFrom = %v, want context.Canceled", err)
	}
}

func TestACancelledContextStartsNoSession(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c := &Client{session: func(...pkgsftp.ClientOption) (*pkgsftp.Client, func() error, error) {
		t.Error("a session was started")
		return nil, nil, errFault
	}}

	if err := c.CopyTo(ctx, "/x", "y"); !errors.Is(err, context.Canceled) {
		t.Errorf("CopyTo = %v, want context.Canceled", err)
	}
}

func TestRequestsAndBufferSizeReachTheSession(t *testing.T) {
	var got int
	c := &Client{Requests: 4, BufferSize: 1 << 16, session: func(opts ...pkgsftp.ClientOption) (*pkgsftp.Client, func() error, error) {
		got = len(opts)
		return nil, nil, errFault
	}}

	c.CopyTo(t.Context(), "/x", "y")
	if got != 2 {
		t.Errorf("session given %d options, want 2", got)
	}
}

// A sink that fails to leave a directory ends the copy.
type leaveFails struct{ scp.Sink }

func (leaveFails) LeaveDir() error { return errFault }

func TestLeavingADirectoryThatFailsEndsTheCopy(t *testing.T) {
	f := newFaults()
	client := faultyClient(f)
	local := t.TempDir()
	if err := os.Mkdir(filepath.Join(local, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	client.Recursive = true
	if err := client.CopyTo(t.Context(), "/d", filepath.Join(local, "d")); err != nil {
		t.Fatal(err)
	}
	sink, err := scp.NewDirSink(t.TempDir(), scp.DirSinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	err = client.with(t.Context(), func(conn *pkgsftp.Client) error {
		_, err := client.copying(conn, leaveFails{sink}).fetchDir("/d", "d", scp.Header{Mode: 0o755, Name: "d"})
		return err
	})
	if !errors.Is(err, errFault) {
		t.Errorf("fetchDir = %v, want the sink's failure", err)
	}
}
