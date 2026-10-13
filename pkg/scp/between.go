package scp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
)

// CopyBetween copies sources on the host src is connected to into target on the
// host dst is connected to, through this program, as "scp -3" does. It starts
// "scp -f" on the first host and "scp -t" on the second, reads each file and
// directory from the first and writes it to the second as it arrives. Nothing is
// written to this machine.
//
// src's settings decide the transfer: Recursive copies directories, Preserve keeps
// times and modes, Glob leaves wildcards in the sources for its shell, MaxSize
// refuses a file that declares more bytes, Limit paces the copy, and the same name
// check as CopyFrom applies unless AnyNames is set. With more than one source,
// target must be an existing directory on the second host.
//
// A file either side cannot handle is reported and skipped, and the copy carries
// on; the error lists every such failure in the words of the side that had it.
func CopyBetween(ctx context.Context, src *Client, sources []string, dst *Client, target string) error {
	if len(sources) == 0 {
		return errors.New("scp: no sources to copy")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	operands := src.remoteOperands(sources)
	fetchFlags, storeFlags := "-f", "-t"
	if src.Recursive {
		fetchFlags, storeFlags = "-r "+fetchFlags, "-r "+storeFlags
	}
	if len(sources) > 1 || src.Glob {
		storeFlags = "-d " + storeFlags
	}
	// The receiving end runs with the sending end's -p, so it expects the time
	// lines it will be given.
	storer := *dst
	storer.Preserve = src.Preserve

	fetch, err := src.start(ctx, src.command(fetchFlags, operands...))
	if err != nil {
		return fmt.Errorf("scp: starting the fetch: %w", err)
	}
	defer fetch.close()
	store, err := storer.start(ctx, storer.command(storeFlags, Quote(target)))
	if err != nil {
		return fmt.Errorf("scp: starting the store: %w", err)
	}
	defer store.close()

	copyErr := func() error {
		toStore := forwardSink{r: bufio.NewReader(store.stdout), w: store.stdin}
		// The receiving end says it is ready before anything is sent to it.
		if err := readStatus(toStore.r); err != nil {
			return err
		}
		var in io.Reader = fetch.stdout
		var back io.Writer = fetch.stdin
		if src.Limit > 0 {
			// Every byte of the copy comes from the first host, so pacing that
			// side paces the whole copy.
			pace := newLimiter(src.Limit)
			in, back = limitedReader{r: fetch.stdout, l: pace}, limitedWriter{w: fetch.stdin, l: pace}
		}
		opts := ReceiveOptions{MaxSize: src.MaxSize, Recursive: src.Recursive, Progress: src.Progress}
		return receiveEntries(bufio.NewReader(in), back, opts, toStore, src.nameCheck(sources))
	}()

	// Both ends are done once their input closes.
	fetch.stdin.Close()
	store.stdin.Close()
	fetchErr := fetch.wait()
	storeErr := store.wait()

	if err := ctx.Err(); err != nil {
		return err
	}
	if copyErr == nil && fetchErr == nil && storeErr == nil {
		return nil
	}
	return errors.Join(copyErr, fetch.failure("fetch", fetchErr), store.failure("store", storeErr))
}

// forwardSink is a Sink that writes what it is given to the receiving end of
// another transfer, speaking the sending side of the protocol to it.
type forwardSink struct {
	r *bufio.Reader
	w io.Writer
}

// File sends one file. The receiving end may turn it down at its control line;
// the file's body is then left for the caller to drain.
func (f forwardSink) File(h Header, body io.Reader) error {
	failure, err := sender{r: f.r, out: f.w}.file(h, body, h.Name)
	if err != nil {
		return err
	}
	return failure
}

func (f forwardSink) EnterDir(h Header) error {
	if err := sendTimes(f.r, f.w, h); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f.w, "D%04o 0 %s\n", h.Mode.Perm(), h.Name); err != nil {
		return err
	}
	return readStatus(f.r)
}

func (f forwardSink) LeaveDir() error {
	if _, err := io.WriteString(f.w, "E\n"); err != nil {
		return err
	}
	return readStatus(f.r)
}
