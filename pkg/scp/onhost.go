package scp

import (
	"context"
	"errors"
	"io"
)

// CopyOnHost runs scp on the host c is connected to, copying sources to target, as
// "scp -R" does. target names the other host the way scp does, as host:path or
// user@host:path, and the sources are paths on the connected host. The files go
// from one host to the other directly and never pass through this program.
//
// The connected host's own scp and SSH setup decide how it reaches the other host:
// its keys, its known hosts, and which protocol its scp speaks. Recursive copies
// directories, Preserve keeps times and modes, and Glob leaves wildcards in the
// sources for the connected host's shell. Limit and MaxSize do not apply, because
// the data does not pass through here.
func (c *Client) CopyOnHost(ctx context.Context, target string, sources ...string) error {
	if len(sources) == 0 {
		return errors.New("scp: no sources to copy")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	operands := append(c.remoteOperands(sources), Quote(target))

	flags := "-q"
	if c.Recursive {
		flags = "-r " + flags
	}
	p, err := c.start(ctx, c.command(flags, operands...))
	if err != nil {
		return err
	}
	defer p.close()

	// scp reads nothing here and its output is not part of the copy.
	p.stdin.Close()
	io.Copy(io.Discard, p.stdout)
	if err := p.wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return p.failure("copy", err)
	}
	return nil
}
