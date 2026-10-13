package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/antimatter-studios/go-scp/pkg/scp"
	pkgsftp "github.com/pkg/sftp"
)

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
	return c.with(ctx, func(client *pkgsftp.Client) error {
		info, err := client.Stat(target)
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
			if err := c.put(client, src, remote); err != nil {
				failures = append(failures, err)
			}
		}
		return errors.Join(failures...)
	})
}

// put copies one local file or directory to remote.
func (c *Client) put(client *pkgsftp.Client, local, remote string) error {
	info, err := os.Stat(local)
	if err != nil {
		return fmt.Errorf("sftp: %w", err)
	}
	switch {
	case info.IsDir() && c.Recursive:
		return c.putDir(client, local, remote, info)
	case info.Mode().IsRegular():
		return c.putFile(client, local, remote, info)
	default:
		return fmt.Errorf("sftp: %s: not a regular file", local)
	}
}

func (c *Client) putFile(client *pkgsftp.Client, local, remote string, info fs.FileInfo) error {
	in, err := os.Open(local)
	if err != nil {
		return fmt.Errorf("sftp: %w", err)
	}
	defer in.Close()
	_, statErr := client.Stat(remote)
	created := errors.Is(statErr, fs.ErrNotExist)
	out, err := client.OpenFile(remote, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("sftp: %s: %w", remote, err)
	}
	// The server creates a file with a mode of its own choosing, which may let
	// others read it, so its mode is set before any of it is written.
	switch {
	case c.Preserve:
		err = out.Chmod(info.Mode().Perm())
	case created:
		err = out.Chmod(info.Mode().Perm() &^ 0o022)
	}
	if err == nil {
		// Copied through plain Write calls: github.com/pkg/sftp's File.ReadFrom
		// drops the error of a write that ends the file part-way through a packet,
		// and reports the copy whole.
		_, err = io.Copy(struct{ io.Writer }{out}, struct{ io.Reader }{in})
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil && c.Preserve {
		err = client.Chtimes(remote, info.ModTime(), info.ModTime())
	}
	if err != nil {
		return fmt.Errorf("sftp: %s: %w", remote, err)
	}
	return nil
}

func (c *Client) putDir(client *pkgsftp.Client, local, remote string, info fs.FileInfo) error {
	entries, err := os.ReadDir(local)
	if err != nil {
		return fmt.Errorf("sftp: %w", err)
	}
	existing, err := client.Stat(remote)
	created := errors.Is(err, fs.ErrNotExist)
	switch {
	case err == nil && !existing.IsDir():
		return fmt.Errorf("sftp: %s: not a directory", remote)
	case created:
		err = client.Mkdir(remote)
	}
	if err != nil {
		return fmt.Errorf("sftp: %s: %w", remote, err)
	}

	var failures []error
	for _, e := range entries {
		if err := c.put(client, filepath.Join(local, e.Name()), path.Join(remote, e.Name())); err != nil {
			failures = append(failures, err)
		}
	}

	// The mode is set once the entries are in, so a directory without write
	// permission still takes them.
	switch {
	case c.Preserve:
		err = client.Chmod(remote, info.Mode().Perm())
		if err == nil {
			err = client.Chtimes(remote, info.ModTime(), info.ModTime())
		}
	case created:
		err = client.Chmod(remote, info.Mode().Perm()&^0o022)
	}
	if err != nil {
		failures = append(failures, fmt.Errorf("sftp: %s: %w", remote, err))
	}
	return errors.Join(failures...)
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
	sink, err := scp.NewDirSink(target, scp.DirSinkOptions{Preserve: c.Preserve})
	if err != nil {
		return err
	}
	defer sink.Close()

	return c.with(ctx, func(client *pkgsftp.Client) error {
		var failures []error
		for _, src := range sources {
			names, err := c.expand(client, remotePath(src))
			if err != nil {
				failures = append(failures, err)
				continue
			}
			for _, name := range names {
				failure, err := c.fetch(client, sink, name)
				if err != nil {
					return errors.Join(append(failures, err)...)
				}
				if failure != nil {
					failures = append(failures, failure)
				}
			}
		}
		return errors.Join(failures...)
	})
}

// expand returns the remote paths src names: itself or, with Glob, its matches.
func (c *Client) expand(client *pkgsftp.Client, src string) ([]string, error) {
	if !c.Glob {
		return []string{src}, nil
	}
	matches, err := client.Glob(src)
	if err == nil && len(matches) == 0 {
		err = fs.ErrNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("sftp: %s: %w", src, err)
	}
	return matches, nil
}

// fetch copies one remote file or directory into sink. It returns the entry's own
// failure, which the copy survives, separately from an error that ends it.
func (c *Client) fetch(client *pkgsftp.Client, sink scp.Sink, remote string) (failure, err error) {
	info, err := client.Stat(remote)
	if err != nil {
		return fmt.Errorf("sftp: %s: %w", remote, err), nil
	}
	h := scp.Header{Mode: info.Mode().Perm(), Size: info.Size(), Name: path.Base(remote)}
	if c.Preserve {
		h.ModTime, h.AccessTime = info.ModTime(), info.ModTime()
	}
	if err := validName(h.Name); err != nil {
		return fmt.Errorf("sftp: %s: %w", remote, err), nil
	}

	switch {
	case info.IsDir() && c.Recursive:
		return c.fetchDir(client, sink, remote, h)
	case info.Mode().IsRegular():
		file, err := client.Open(remote)
		if err != nil {
			return fmt.Errorf("sftp: %s: %w", remote, err), nil
		}
		defer file.Close()
		if err := sink.File(h, file); err != nil {
			return fmt.Errorf("sftp: %s: %w", remote, err), nil
		}
		return nil, nil
	default:
		return fmt.Errorf("sftp: %s: not a regular file", remote), nil
	}
}

func (c *Client) fetchDir(client *pkgsftp.Client, sink scp.Sink, remote string, h scp.Header) (failure, err error) {
	entries, err := client.ReadDir(remote)
	if err != nil {
		return fmt.Errorf("sftp: %s: %w", remote, err), nil
	}
	if err := sink.EnterDir(h); err != nil {
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
		entryFailure, err := c.fetch(client, sink, path.Join(remote, e.Name()))
		if err != nil {
			return nil, err
		}
		if entryFailure != nil {
			failures = append(failures, entryFailure)
		}
	}
	if err := sink.LeaveDir(); err != nil {
		return nil, err
	}
	return errors.Join(failures...), nil
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
