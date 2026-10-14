package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"

	"github.com/antimatter-studios/go-scp/pkg/scp"
	pkgsftp "github.com/pkg/sftp"
)

// CopyBetween copies sources on the host src reaches into target on the host dst
// reaches, through this program, as "scp -3" does over SFTP. Each file is read from
// one session and written to the other as it arrives; nothing is written to this
// machine.
//
// src's settings decide the copy: Recursive copies directories, Preserve keeps
// times and modes, Glob matches wildcards in the sources, MaxSize refuses a larger
// file, and Limit paces the copy. With more than one source, or with Glob, target
// must be an existing directory on the second host; otherwise target is written
// into when it is a directory and as the copy itself when it is not, as CopyFrom
// does locally.
//
// A file either side cannot handle is reported and skipped, and the copy carries
// on; the error lists every such failure.
func CopyBetween(ctx context.Context, src *Client, sources []string, dst *Client, target string) error {
	if len(sources) == 0 {
		return errors.New("sftp: no sources to copy")
	}
	target = remotePath(target)
	return src.with(ctx, func(from *pkgsftp.Client) error {
		return dst.with(ctx, func(to *pkgsftp.Client) error {
			info, err := to.Stat(target)
			intoDir := err == nil && info.IsDir()
			if (len(sources) > 1 || src.Glob) && !intoDir {
				return fmt.Errorf("sftp: %s: must be an existing directory to take several files", target)
			}
			sink := &remoteSink{
				w:       remoteWriter{conn: to, preserve: src.Preserve},
				target:  target,
				intoDir: intoDir,
			}
			return src.copying(from, sink).fetchAll(sources)
		})
	})
}

// remoteSink is an scp.Sink that writes to a remote host over SFTP, the way
// scp.DirSink writes locally: into target when it is a directory, and as target
// itself when it is not.
type remoteSink struct {
	w       remoteWriter
	target  string
	intoDir bool
	dirs    []remoteDir // the directories entered and not yet left, innermost last
}

type remoteDir struct {
	path    string
	h       scp.Header
	created bool
}

// place returns where an entry named name goes. CopyBetween allows only one
// top-level entry when target is not a directory, so that entry is target itself.
func (s *remoteSink) place(name string) string {
	switch {
	case len(s.dirs) > 0:
		return path.Join(s.dirs[len(s.dirs)-1].path, name)
	case s.intoDir:
		return path.Join(s.target, name)
	default:
		return s.target
	}
}

func (s *remoteSink) File(h scp.Header, body io.Reader) error {
	return s.w.writeFile(s.place(h.Name), h, body, 0)
}

func (s *remoteSink) EnterDir(h scp.Header) error {
	p := s.place(h.Name)
	created, err := s.w.openDir(p)
	if err != nil {
		return err
	}
	s.dirs = append(s.dirs, remoteDir{path: p, h: h, created: created})
	return nil
}

func (s *remoteSink) LeaveDir() error {
	d := s.dirs[len(s.dirs)-1]
	s.dirs = s.dirs[:len(s.dirs)-1]
	return s.w.closeDir(d.path, d.h, d.created)
}
