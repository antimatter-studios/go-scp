package scp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// CopyTo copies local files to target on the remote host, as
// "scp [-r] [-p] source... host:target" does. With Recursive a source may be a
// directory, copied with its contents. With more than one source, target must be
// an existing remote directory.
//
// A source that cannot be sent, or that the remote refuses, is skipped; the rest
// are copied and the error lists every failure.
func (c *Client) CopyTo(ctx context.Context, target string, sources ...string) error {
	if len(sources) == 0 {
		return errors.New("scp: no sources to copy")
	}
	items := make([]source, len(sources))
	for i, src := range sources {
		src = filepath.Clean(src)
		items[i] = source{fsys: os.DirFS(filepath.Dir(src)), name: filepath.Base(src)}
	}
	flags := "-t"
	if c.Recursive {
		flags = "-r " + flags
	}
	if len(sources) > 1 {
		flags = "-d " + flags // the remote refuses a target that is not a directory
	}
	opts := SendOptions{Recursive: c.Recursive, Preserve: c.Preserve, Progress: c.Progress}
	return c.run(ctx, c.command(flags, Quote(target)), func(r *bufio.Reader, out io.Writer) error {
		return sendEntries(r, out, items, opts)
	})
}

// CopyFrom copies files from the remote host into target on this host, as
// "scp [-r] [-p] host:source... target" does. With Recursive a source may be a
// remote directory, copied with its contents. With more than one source, target
// must be an existing local directory. Files are written the way DirSink writes
// them, and never outside target.
//
// The remote may send only the names asked for: each top-level entry must be the
// last element of one of the sources, or match it when Glob is set. AnyNames
// relaxes that, as "scp -T" does.
func (c *Client) CopyFrom(ctx context.Context, target string, sources ...string) error {
	if len(sources) == 0 {
		return errors.New("scp: no sources to copy")
	}
	if len(sources) > 1 || c.Glob {
		if info, err := os.Stat(target); err != nil || !info.IsDir() {
			return fmt.Errorf("scp: %s: must be an existing directory to take several files", target)
		}
	}
	sink, err := NewDirSink(target, DirSinkOptions{Preserve: c.Preserve})
	if err != nil {
		return err
	}
	defer sink.Close()

	operands := c.remoteOperands(sources)
	flags := "-f"
	if c.Recursive {
		flags = "-r " + flags
	}
	opts := ReceiveOptions{MaxSize: c.MaxSize, Recursive: c.Recursive, Progress: c.Progress}
	check := c.nameCheck(sources)
	return c.run(ctx, c.command(flags, operands...), func(r *bufio.Reader, out io.Writer) error {
		return receiveEntries(r, out, opts, sink, check)
	})
}

// nameCheck accepts, at the top level, only names that the sources asked for.
func (c *Client) nameCheck(sources []string) nameCheck {
	if c.AnyNames {
		return nil
	}
	wanted := make([]string, len(sources))
	for i, s := range sources {
		wanted[i] = path.Base(s)
	}
	return func(name string, depth int) error {
		if depth > 0 {
			return nil
		}
		for _, w := range wanted {
			if name == w {
				return nil
			}
			if c.Glob {
				if ok, _ := path.Match(w, name); ok {
					return nil
				}
			}
		}
		return fmt.Errorf("scp: the remote sent %q, which was not asked for", name)
	}
}

// remoteOperands quotes remote paths for the remote shell, each as one word, or,
// with Glob, leaving their wildcards for the shell to expand.
func (c *Client) remoteOperands(paths []string) []string {
	operands := make([]string, len(paths))
	for i, p := range paths {
		if c.Glob {
			operands[i] = quotePattern(p)
		} else {
			operands[i] = Quote(p)
		}
	}
	return operands
}

// quotePattern quotes s for the remote shell like Quote, except for wildcards
// (*, ? and bracket expressions), which are left for the shell to expand.
func quotePattern(s string) string {
	var b, literal strings.Builder
	flush := func() {
		if literal.Len() > 0 {
			b.WriteString(Quote(literal.String()))
			literal.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '*', '?':
			flush()
			b.WriteByte(s[i])
		case '[':
			end := strings.IndexByte(s[i+1:], ']')
			if end < 0 {
				literal.WriteByte(s[i])
				continue
			}
			flush()
			b.WriteString(s[i : i+end+2])
			i += end + 1
		default:
			literal.WriteByte(s[i])
		}
	}
	flush()
	return b.String()
}
