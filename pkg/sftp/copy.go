package sftp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/antimatter-studios/go-scp/pkg/scp"
	pkgsftp "github.com/pkg/sftp"
)

// copying is one copy in progress: the client's settings, the SFTP session it runs
// over, the pace it keeps, and, for a copy from the remote host, where it writes.
// files and progress are the client's Parallel and Progress made ready for files
// that copy at once; see files.go.
type copying struct {
	*Client
	conn     *pkgsftp.Client
	pace     *pacer
	sink     scp.Sink
	files    chan struct{}
	progress scp.Progress
}

func (c *Client) copying(conn *pkgsftp.Client, sink scp.Sink) *copying {
	cp := &copying{Client: c, conn: conn, pace: newPacer(c.Limit), sink: sink, progress: c.Progress}
	cp.parallel()
	return cp
}

// CopyTo copies local files to target on the remote host, as
// "scp -s [-r] [-p] source... host:target" does. With Recursive a source may be a
// directory, copied with its contents. With more than one source, target must be
// an existing remote directory.
//
// A new file takes the source's mode, less 022 as a server would take its umask
// off, before any of it is written; an existing file keeps its own. With Preserve
// both take the source's mode and modification time exactly. With Atomic every
// file is written as a new one beside its target and renamed over it once whole,
// which needs the server's posix-rename@openssh.com extension.
//
// A source that cannot be copied is skipped; the rest are copied and the error
// lists every failure, each an scp.FileError that scp.Failures finds.
func (c *Client) CopyTo(ctx context.Context, target string, sources ...string) error {
	if len(sources) == 0 {
		return errors.New("sftp: no sources to copy")
	}
	target = remotePath(target)
	return c.with(ctx, func(conn *pkgsftp.Client) error {
		cp := c.copying(conn, nil)
		info, err := conn.Stat(target)
		intoDir := err == nil && info.IsDir()
		if len(sources) > 1 && !intoDir {
			return fmt.Errorf("sftp: %s: must be an existing directory to take several files", target)
		}
		failures := make([]error, len(sources))
		var inFlight sync.WaitGroup
		for i, src := range sources {
			src = filepath.Clean(src)
			remote := target
			if intoDir {
				remote = path.Join(target, filepath.Base(src))
			}
			cp.put(&inFlight, src, remote, filepath.Base(src), nil, &failures[i])
		}
		inFlight.Wait()
		return errors.Join(failures...)
	})
}

// put copies one local file or directory to remote, and sets *failure to its
// failure; name is its path in the copy, as Progress is told it, and above the
// directories it was reached through, outermost first. A file may still be on its
// way when put returns, counted in inFlight.
func (cp *copying) put(inFlight *sync.WaitGroup, local, remote, name string, above []fs.FileInfo, failure *error) {
	info, err := os.Stat(local)
	switch {
	case err != nil:
		*failure = skipped(name, fmt.Errorf("sftp: %w", err))
	case info.IsDir() && cp.Recursive && insideItself(info, above):
		*failure = skipped(name, fmt.Errorf("sftp: %s: a link back to a directory it is inside; not followed", local))
	case info.IsDir() && cp.Recursive:
		*failure = cp.putDir(local, remote, name, append(above, info))
	case info.Mode().IsRegular():
		cp.file(inFlight, func() {
			if err := cp.putFile(local, remote, name, info); err != nil {
				*failure = skipped(name, err)
			}
		})
	default:
		*failure = skipped(name, fmt.Errorf("sftp: %s: not a regular file", local))
	}
}

func (cp *copying) putFile(local, remote, name string, info fs.FileInfo) error {
	in, err := os.Open(local)
	if err != nil {
		return fmt.Errorf("sftp: %w", err)
	}
	defer in.Close()
	w, h := cp.remote(), localHeader(info)
	offset, err := w.resumeFrom(remote, h)
	if err == nil {
		_, err = in.Seek(offset, io.SeekStart)
	}
	if err != nil {
		return err
	}
	return w.writeFile(remote, h, cp.progress.Track(name, offset, h.Size, cp.pace.reader(in)), offset)
}

// putDir copies the local directory at local, the last of above, with everything in
// it.
func (cp *copying) putDir(local, remote, name string, above []fs.FileInfo) error {
	info := above[len(above)-1]
	entries, err := os.ReadDir(local)
	if err != nil {
		return skipped(name, fmt.Errorf("sftp: %w", err))
	}
	w := cp.remote()
	created, err := w.openDir(remote)
	if err != nil {
		return skipped(name, err)
	}
	// The directory's own failure, if closing it fails, comes after its entries'.
	failures := make([]error, len(entries)+1)
	var inFlight sync.WaitGroup
	for i, e := range entries {
		cp.put(&inFlight, filepath.Join(local, e.Name()), path.Join(remote, e.Name()), path.Join(name, e.Name()), above, &failures[i])
	}
	// Its mode and times go on once everything in it is written.
	inFlight.Wait()
	if err := w.closeDir(remote, localHeader(info), created); err != nil {
		failures[len(entries)] = skipped(name, err)
	}
	return errors.Join(failures...)
}

// insideItself reports whether the directory dir is one of above, the directories
// it was reached through: a link back to one of them would copy the same tree
// inside itself again and again.
func insideItself(dir fs.FileInfo, above []fs.FileInfo) bool {
	for _, a := range above {
		if os.SameFile(a, dir) {
			return true
		}
	}
	return false
}

// localHeader describes a local file or directory as the remote end writes it:
// its mode, size and times.
func localHeader(info fs.FileInfo) scp.Header {
	return scp.Header{Mode: info.Mode().Perm(), Size: info.Size(), ModTime: info.ModTime(), AccessTime: scp.AccessTime(info)}
}

// remote is the copy's session as the receiving end of the copy.
func (cp *copying) remote() remoteWriter {
	return remoteWriter{conn: cp.conn, preserve: cp.Preserve, resume: cp.Resume, atomic: cp.Atomic}
}

// CopyFrom copies files from the remote host into target on this host, as
// "scp -s [-r] [-p] host:source... target" does. With Recursive a source may be a
// remote directory, copied with its contents. With more than one source, or with
// Glob, target must be an existing local directory. Files are written the way
// scp.DirSink writes them, and never outside target.
//
// A source that cannot be copied is skipped; the rest are copied and the error
// lists every failure, each an scp.FileError that scp.Failures finds. A directory
// that cannot be created here ends the copy.
func (c *Client) CopyFrom(ctx context.Context, target string, sources ...string) error {
	if len(sources) == 0 {
		return errors.New("sftp: no sources to copy")
	}
	if len(sources) > 1 || c.Glob {
		if info, err := os.Stat(target); err != nil || !info.IsDir() {
			return fmt.Errorf("sftp: %s: must be an existing directory to take several files", target)
		}
	}
	sink, err := scp.NewDirSink(target, scp.DirSinkOptions{Preserve: c.Preserve, Resume: c.Resume, Atomic: c.Atomic})
	if err != nil {
		return err
	}
	defer sink.Close()

	return c.with(ctx, func(conn *pkgsftp.Client) error {
		return c.copying(conn, sink).fetchAll(sources)
	})
}

// fetchAll copies every source, each expanded when Glob is set, into the sink.
func (cp *copying) fetchAll(sources []string) error {
	var failures []error
	for _, src := range sources {
		remotes, err := cp.expand(remotePath(src))
		if err != nil {
			failures = append(failures, skipped(remoteBase(src), err))
			continue
		}
		found := make([]error, len(remotes))
		err = cp.fetchEach(remotes, "", found)
		failures = append(failures, found...)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	return errors.Join(failures...)
}

// expand returns the remote paths src names: itself or, with Glob, its matches.
func (cp *copying) expand(src string) ([]string, error) {
	if !cp.Glob {
		return []string{src}, nil
	}
	matches, err := cp.conn.Glob(src)
	if err == nil && len(matches) == 0 {
		err = fs.ErrNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("sftp: %s: %w", src, err)
	}
	return matches, nil
}

// fetchEach copies the remote files and directories at remotes, the entries of one
// directory or the top-level sources, into the sink, from the directory whose path
// in the copy is parent; "" for the top. It puts each entry's own failure at its
// index in failures, and leaves alone an index whose path is "", which already
// holds one. It returns an error that ends the copy.
//
// Files may be copied several at once; a directory is entered only once the files
// before it are in, since entering it changes where the sink puts what it is given.
func (cp *copying) fetchEach(remotes []string, parent string, failures []error) error {
	var inFlight sync.WaitGroup
	defer inFlight.Wait()
	for i, remote := range remotes {
		if remote == "" {
			continue
		}
		e, failure := cp.find(remote, parent)
		switch {
		case failure != nil:
			failures[i] = failure
		case e.dir:
			inFlight.Wait()
			var err error
			if failures[i], err = cp.fetchDir(e); err != nil {
				return err
			}
		default:
			cp.file(&inFlight, func() {
				if err := cp.fetchFile(e); err != nil {
					failures[i] = skipped(e.name, err)
				}
			})
		}
	}
	return nil
}

// found is a remote file or directory a copy has found: where it is, its path in
// the copy, its header, and whether it is a directory to copy with its contents.
type found struct {
	remote string
	name   string
	h      scp.Header
	dir    bool
}

// find looks up the remote file or directory at remote, in the directory whose
// path in the copy is parent. It returns the entry's failure when it cannot be
// copied at all.
func (cp *copying) find(remote, parent string) (found, error) {
	h := scp.Header{Name: remoteBase(remote)}
	if err := validName(h.Name); err != nil {
		return found{}, skipped("", fmt.Errorf("sftp: %s: %w", remote, err))
	}
	name := path.Join(parent, h.Name)
	info, err := cp.conn.Stat(remote)
	if err != nil {
		return found{}, skipped(name, fmt.Errorf("sftp: %s: %w", remote, err))
	}
	h.Mode, h.Size = info.Mode().Perm(), info.Size()
	if cp.Preserve {
		// github.com/pkg/sftp's Stat always gives a *FileStat, with the access
		// time the server sent along with the modification time.
		h.ModTime = info.ModTime()
		h.AccessTime = time.Unix(int64(info.Sys().(*pkgsftp.FileStat).Atime), 0)
	}
	e := found{remote: remote, name: name, h: h, dir: info.IsDir() && cp.Recursive}
	if !e.dir && !info.Mode().IsRegular() {
		return found{}, skipped(name, fmt.Errorf("sftp: %s: not a regular file", remote))
	}
	return e, nil
}

// fetchFile copies the remote regular file e into the sink.
func (cp *copying) fetchFile(e found) error {
	if cp.MaxSize > 0 && e.h.Size > cp.MaxSize {
		return fmt.Errorf("sftp: %s is %d bytes, more than the %d this client accepts", e.remote, e.h.Size, cp.MaxSize)
	}
	offset, err := cp.resumeFrom(e.h)
	if err != nil {
		return fmt.Errorf("sftp: %s: %w", e.remote, err)
	}
	file, err := cp.conn.Open(e.remote)
	if err != nil {
		return fmt.Errorf("sftp: %s: %w", e.remote, err)
	}
	defer file.Close()
	// From offset on, read ahead by every request a file may have in flight, so
	// they go out together rather than one per round trip.
	rest := io.NewSectionReader(file, offset, e.h.Size-offset)
	body := bufio.NewReaderSize(cp.pace.reader(rest), cp.readAhead())
	if err := cp.sink.File(e.h, cp.progress.Track(e.name, offset, e.h.Size, body)); err != nil {
		return fmt.Errorf("sftp: %s: %w", e.remote, err)
	}
	return nil
}

// fetchDir copies the remote directory e with everything in it into the sink. It
// returns the failures of what is in it, which the copy survives, separately from
// an error that ends it.
func (cp *copying) fetchDir(e found) (failure, err error) {
	entries, err := cp.conn.ReadDir(e.remote)
	if err != nil {
		return skipped(e.name, fmt.Errorf("sftp: %s: %w", e.remote, err)), nil
	}
	if err := cp.sink.EnterDir(e.h); err != nil {
		return nil, err
	}
	remotes := make([]string, len(entries))
	failures := make([]error, len(entries))
	for i, entry := range entries {
		// The server names the entries; a name that is not one plain name would
		// lead somewhere other than inside this directory.
		if err := validName(entry.Name()); err != nil {
			failures[i] = skipped("", fmt.Errorf("sftp: %s: the server listed %w", e.remote, err))
			continue
		}
		remotes[i] = path.Join(e.remote, entry.Name())
	}
	if err := cp.fetchEach(remotes, e.name, failures); err != nil {
		return nil, err
	}
	if err := cp.sink.LeaveDir(); err != nil {
		return nil, err
	}
	return errors.Join(failures...), nil
}

// github.com/pkg/sftp's defaults for the requests one file may have in flight and
// the most one request carries.
const (
	defaultRequests = 64
	defaultPacket   = 32 * 1024
)

// resumeFrom is where the sink continues the file h describes: what it already
// holds when Resume is set and it can say, and 0 otherwise.
func (cp *copying) resumeFrom(h scp.Header) (int64, error) {
	resumer, ok := cp.sink.(interface {
		ResumeFrom(scp.Header) (int64, error)
	})
	if !cp.Resume || !ok {
		return 0, nil
	}
	return resumer.ResumeFrom(h)
}

// readAhead is how much of a remote file to ask for at once: a full packet for
// every request a file may have in flight, which github.com/pkg/sftp then sends
// together.
func (c *Client) readAhead() int {
	requests, packet := c.Requests, c.BufferSize
	if requests <= 0 {
		requests = defaultRequests
	}
	if packet <= 0 {
		packet = defaultPacket
	}
	return requests * packet
}

// skipped returns err as the failure of the entry at name, the entry's path in the
// copy, which scp.Failures finds.
func skipped(name string, err error) error {
	return &scp.FileError{Path: name, Err: err}
}

// remoteBase is the last element of a remote path, split at a slash or at a
// backslash, since the remote host may be either kind.
func remoteBase(p string) string {
	return path.Base(strings.ReplaceAll(p, `\`, "/"))
}

// validName checks that name is one plain file name here; which characters break
// a name depends on the system, see nameBreaks.
func validName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, nameBreaks) {
		return fmt.Errorf("%q, which is not a plain file name", name)
	}
	return nil
}

// remotePath turns a leading ~ into the login directory, which is where an SFTP
// session starts, as a shell would for scp's remote paths: ~/x is x and ~ is ".".
func remotePath(p string) string {
	switch {
	case p == "~":
		return "."
	case strings.HasPrefix(p, "~/"):
		if rest := strings.TrimLeft(p[2:], "/"); rest != "" {
			return rest
		}
		return "."
	default:
		return p
	}
}
