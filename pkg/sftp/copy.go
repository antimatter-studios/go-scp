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
	"time"

	"github.com/antimatter-studios/go-scp/pkg/scp"
	pkgsftp "github.com/pkg/sftp"
)

// copying is one copy in progress: the client's settings, the SFTP session it runs
// over, the pace it keeps, and, for a copy from the remote host, where it writes.
type copying struct {
	*Client
	conn *pkgsftp.Client
	pace *pacer
	sink scp.Sink
}

func (c *Client) copying(conn *pkgsftp.Client, sink scp.Sink) *copying {
	return &copying{Client: c, conn: conn, pace: newPacer(c.Limit), sink: sink}
}

// CopyTo copies local files to target on the remote host, as
// "scp -s [-r] [-p] source... host:target" does. With Recursive a source may be a
// directory, copied with its contents. With more than one source, target must be
// an existing remote directory.
//
// A new file takes the source's mode, less 022 as a server would take its umask
// off, before any of it is written; an existing file keeps its own. With Preserve
// both take the source's mode and modification time exactly.
//
// A source that cannot be copied is skipped; the rest are copied and the error
// lists every failure.
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
		var failures []error
		for _, src := range sources {
			src = filepath.Clean(src)
			remote := target
			if intoDir {
				remote = path.Join(target, filepath.Base(src))
			}
			if err := cp.put(src, remote); err != nil {
				failures = append(failures, err)
			}
		}
		return errors.Join(failures...)
	})
}

// put copies one local file or directory to remote.
func (cp *copying) put(local, remote string) error {
	info, err := os.Stat(local)
	if err != nil {
		return fmt.Errorf("sftp: %w", err)
	}
	switch {
	case info.IsDir() && cp.Recursive:
		return cp.putDir(local, remote, info)
	case info.Mode().IsRegular():
		return cp.putFile(local, remote, info)
	default:
		return fmt.Errorf("sftp: %s: not a regular file", local)
	}
}

func (cp *copying) putFile(local, remote string, info fs.FileInfo) error {
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
	return w.writeFile(remote, h, cp.pace.reader(in), offset)
}

func (cp *copying) putDir(local, remote string, info fs.FileInfo) error {
	entries, err := os.ReadDir(local)
	if err != nil {
		return fmt.Errorf("sftp: %w", err)
	}
	w := cp.remote()
	created, err := w.openDir(remote)
	if err != nil {
		return err
	}
	var failures []error
	for _, e := range entries {
		if err := cp.put(filepath.Join(local, e.Name()), path.Join(remote, e.Name())); err != nil {
			failures = append(failures, err)
		}
	}
	if err := w.closeDir(remote, localHeader(info), created); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// localHeader describes a local file or directory as the remote end writes it:
// its mode, size and times.
func localHeader(info fs.FileInfo) scp.Header {
	return scp.Header{Mode: info.Mode().Perm(), Size: info.Size(), ModTime: info.ModTime(), AccessTime: scp.AccessTime(info)}
}

// remote is the copy's session as the receiving end of the copy.
func (cp *copying) remote() remoteWriter {
	return remoteWriter{conn: cp.conn, preserve: cp.Preserve, resume: cp.Resume}
}

// CopyFrom copies files from the remote host into target on this host, as
// "scp -s [-r] [-p] host:source... target" does. With Recursive a source may be a
// remote directory, copied with its contents. With more than one source, or with
// Glob, target must be an existing local directory. Files are written the way
// scp.DirSink writes them, and never outside target.
//
// A source that cannot be copied is skipped; the rest are copied and the error
// lists every failure. A directory that cannot be created here ends the copy.
func (c *Client) CopyFrom(ctx context.Context, target string, sources ...string) error {
	if len(sources) == 0 {
		return errors.New("sftp: no sources to copy")
	}
	if len(sources) > 1 || c.Glob {
		if info, err := os.Stat(target); err != nil || !info.IsDir() {
			return fmt.Errorf("sftp: %s: must be an existing directory to take several files", target)
		}
	}
	sink, err := scp.NewDirSink(target, scp.DirSinkOptions{Preserve: c.Preserve, Resume: c.Resume})
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
		names, err := cp.expand(remotePath(src))
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, name := range names {
			failure, err := cp.fetch(name)
			if err != nil {
				return errors.Join(append(failures, err)...)
			}
			if failure != nil {
				failures = append(failures, failure)
			}
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

// fetch copies one remote file or directory into the sink. It returns the entry's
// own failure, which the copy survives, separately from an error that ends it.
func (cp *copying) fetch(remote string) (failure, err error) {
	info, err := cp.conn.Stat(remote)
	if err != nil {
		return fmt.Errorf("sftp: %s: %w", remote, err), nil
	}
	h := scp.Header{Mode: info.Mode().Perm(), Size: info.Size(), Name: path.Base(remote)}
	if cp.Preserve {
		// github.com/pkg/sftp's Stat always gives a *FileStat, with the access
		// time the server sent along with the modification time.
		h.ModTime = info.ModTime()
		h.AccessTime = time.Unix(int64(info.Sys().(*pkgsftp.FileStat).Atime), 0)
	}
	if err := validName(h.Name); err != nil {
		return fmt.Errorf("sftp: %s: %w", remote, err), nil
	}

	switch {
	case info.IsDir() && cp.Recursive:
		return cp.fetchDir(remote, h)
	case info.Mode().IsRegular():
		if cp.MaxSize > 0 && h.Size > cp.MaxSize {
			return fmt.Errorf("sftp: %s is %d bytes, more than the %d this client accepts", remote, h.Size, cp.MaxSize), nil
		}
		offset, err := cp.resumeFrom(h)
		if err != nil {
			return fmt.Errorf("sftp: %s: %w", remote, err), nil
		}
		file, err := cp.conn.Open(remote)
		if err != nil {
			return fmt.Errorf("sftp: %s: %w", remote, err), nil
		}
		defer file.Close()
		// From offset on, read ahead by every request a file may have in flight,
		// so they go out together rather than one per round trip.
		rest := io.NewSectionReader(file, offset, h.Size-offset)
		body := bufio.NewReaderSize(cp.pace.reader(rest), cp.readAhead())
		if err := cp.sink.File(h, body); err != nil {
			return fmt.Errorf("sftp: %s: %w", remote, err), nil
		}
		return nil, nil
	default:
		return fmt.Errorf("sftp: %s: not a regular file", remote), nil
	}
}

func (cp *copying) fetchDir(remote string, h scp.Header) (failure, err error) {
	entries, err := cp.conn.ReadDir(remote)
	if err != nil {
		return fmt.Errorf("sftp: %s: %w", remote, err), nil
	}
	if err := cp.sink.EnterDir(h); err != nil {
		return nil, err
	}
	var failures []error
	for _, e := range entries {
		// The server names the entries; a name that is not one plain name would
		// lead somewhere other than inside this directory.
		if err := validName(e.Name()); err != nil {
			failures = append(failures, fmt.Errorf("sftp: %s: the server listed %w", remote, err))
			continue
		}
		entryFailure, err := cp.fetch(path.Join(remote, e.Name()))
		if err != nil {
			return nil, err
		}
		if entryFailure != nil {
			failures = append(failures, entryFailure)
		}
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

// validName checks that name is one plain file name.
func validName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\n") {
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
