package scp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
)

// proc is an scp started on a session of its own, with its input and output open
// for whoever drives it.
type proc struct {
	sess   session
	stdin  io.WriteCloser
	stdout io.Reader

	stderr     bytes.Buffer // read only after wait, once stderrDone is closed
	stderrDone chan struct{}
	stop       func() bool
}

// start runs command on a new session. Cancelling ctx closes the session.
func (c *Client) start(ctx context.Context, command string) (*proc, error) {
	sess, err := c.openSession()
	if err != nil {
		return nil, err
	}
	p := &proc{sess: sess, stderrDone: make(chan struct{})}
	if p.stdin, err = sess.StdinPipe(); err != nil {
		sess.Close()
		return nil, err
	}
	if p.stdout, err = sess.StdoutPipe(); err != nil {
		sess.Close()
		return nil, err
	}
	stderrPipe, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	go func() {
		io.Copy(&p.stderr, stderrPipe)
		close(p.stderrDone)
	}()
	p.stop = context.AfterFunc(ctx, func() { sess.Close() })
	if err := sess.Start(command); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

// wait returns once the remote program has exited and its stderr is collected.
func (p *proc) wait() error {
	err := p.sess.Wait()
	<-p.stderrDone
	return err
}

func (p *proc) close() {
	p.stop()
	p.sess.Close()
}

// failure describes one side of a copy between hosts that went wrong: its error,
// if it had one, and whatever it wrote to stderr. A side can exit cleanly and
// still have the reason on its stderr, because scp prints the failures the other
// side sends it.
func (p *proc) failure(side string, err error) error {
	msg := strings.TrimSpace(p.stderr.String())
	switch {
	case err != nil && msg != "":
		return fmt.Errorf("scp: %s: %w: %s", side, err, msg)
	case err != nil:
		return fmt.Errorf("scp: %s: %w", side, err)
	case msg != "":
		return fmt.Errorf("scp: %s: %s", side, msg)
	default:
		return nil
	}
}
