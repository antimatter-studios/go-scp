package scp

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

var errScripted = errors.New("scripted failure")

// scriptedSession stands in for an SSH session. fail names the step that fails;
// stdout is what the remote scp says; onWait runs as the session is waited on.
type scriptedSession struct {
	fail    string // "stdin", "stdout", "stderr" or "start"
	stdout  string
	stderr  string
	waitErr error
	onWait  func()
}

type discardCloser struct{ io.Writer }

func (discardCloser) Close() error { return nil }

func (s *scriptedSession) StdinPipe() (io.WriteCloser, error) {
	if s.fail == "stdin" {
		return nil, errScripted
	}
	return discardCloser{io.Discard}, nil
}

func (s *scriptedSession) StdoutPipe() (io.Reader, error) {
	if s.fail == "stdout" {
		return nil, errScripted
	}
	return strings.NewReader(s.stdout), nil
}

func (s *scriptedSession) StderrPipe() (io.Reader, error) {
	if s.fail == "stderr" {
		return nil, errScripted
	}
	return strings.NewReader(s.stderr), nil
}

func (s *scriptedSession) Start(string) error {
	if s.fail == "start" {
		return errScripted
	}
	return nil
}

func (s *scriptedSession) Wait() error {
	if s.onWait != nil {
		s.onWait()
	}
	return s.waitErr
}

func (s *scriptedSession) Close() error { return nil }

// scripted returns a client whose sessions are the given ones, in order.
func scripted(sessions ...*scriptedSession) *Client {
	next := 0
	return &Client{newSession: func() (session, error) {
		if next >= len(sessions) {
			return nil, errScripted
		}
		next++
		return sessions[next-1], nil
	}}
}

func TestASessionThatCannotOpenIsAnError(t *testing.T) {
	c := scripted() // no sessions to hand out

	if _, err := c.ReadFile(t.Context(), "/x"); !errors.Is(err, errScripted) {
		t.Errorf("ReadFile = %v", err)
	}
	if err := c.CopyOnHost(t.Context(), "b:/x", "/x"); !errors.Is(err, errScripted) {
		t.Errorf("CopyOnHost = %v", err)
	}
}

func TestEverySessionSetupFailureIsReturned(t *testing.T) {
	for _, step := range []string{"stdin", "stdout", "stderr", "start"} {
		t.Run(step, func(t *testing.T) {
			if _, err := scripted(&scriptedSession{fail: step}).ReadFile(t.Context(), "/x"); !errors.Is(err, errScripted) {
				t.Errorf("ReadFile = %v", err)
			}
			if err := scripted(&scriptedSession{fail: step}).CopyOnHost(t.Context(), "b:/x", "/x"); !errors.Is(err, errScripted) {
				t.Errorf("CopyOnHost = %v", err)
			}
		})
	}
}

func TestAFailedExitAfterAGoodTransferIsReturned(t *testing.T) {
	c := scripted(&scriptedSession{stdout: "C0644 1 x\nX\x00", waitErr: errScripted})

	if _, err := c.ReadFile(t.Context(), "/x"); !errors.Is(err, errScripted) {
		t.Errorf("ReadFile = %v, want the exit failure", err)
	}
}

func TestCancellingWhileWaitingReturnsTheContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := scripted(&scriptedSession{stdout: "C0644 1 x\nX\x00", waitErr: errScripted, onWait: cancel})

	if _, err := c.ReadFile(ctx, "/x"); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadFile = %v, want context.Canceled", err)
	}

	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	c2 := scripted(&scriptedSession{waitErr: errScripted, onWait: cancel2})
	if err := c2.CopyOnHost(ctx2, "b:/x", "/x"); !errors.Is(err, context.Canceled) {
		t.Errorf("CopyOnHost = %v, want context.Canceled", err)
	}
}

func TestCopyBetweenReportsEachSideThatCannotStart(t *testing.T) {
	ok := &scriptedSession{}
	for name, pair := range map[string][2]*Client{
		"first host":  {scripted(), scripted(ok)},
		"second host": {scripted(&scriptedSession{}), scripted()},
	} {
		t.Run(name, func(t *testing.T) {
			if err := CopyBetween(t.Context(), pair[0], []string{"/x"}, pair[1], "/y"); !errors.Is(err, errScripted) {
				t.Errorf("CopyBetween = %v", err)
			}
		})
	}
}

func TestCopyBetweenStopsWhenTheSecondHostIsNotReady(t *testing.T) {
	src := scripted(&scriptedSession{})
	dst := scripted(&scriptedSession{stdout: "\x01scp: /y: No such file or directory\n"})

	err := CopyBetween(t.Context(), src, []string{"/x"}, dst, "/y")

	var remote *RemoteError
	if !errors.As(err, &remote) || !strings.Contains(remote.Message, "No such file") {
		t.Errorf("CopyBetween = %v, want the second host's report", err)
	}
}

func TestCopyBetweenReturnsTheContextErrorWhenCancelledWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	src := scripted(&scriptedSession{onWait: cancel})
	dst := scripted(&scriptedSession{stdout: "\x00"})

	if err := CopyBetween(ctx, src, []string{"/x"}, dst, "/y"); !errors.Is(err, context.Canceled) {
		t.Errorf("CopyBetween = %v, want context.Canceled", err)
	}
}

func TestAFailureWithNoStderrIsJustTheError(t *testing.T) {
	p := &proc{}

	if err := p.failure("copy", errScripted); !errors.Is(err, errScripted) || err.Error() != "scp: copy: scripted failure" {
		t.Errorf("failure = %v", err)
	}
}
