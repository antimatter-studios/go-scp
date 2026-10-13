package scp

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

var errScripted = errors.New("scripted failure")

// scriptedSession stands in for an SSH session. fail names the step that fails;
// stdout is what the remote scp says; onWait runs as the session is waited on.
type scriptedSession struct {
	fail    string // "request", "stdin", "stdout", "stderr" or "start"
	stdout  string
	stderr  string
	waitErr error
	onWait  func()

	// stdinFails makes the session's stdin fail once stdinWritesOK writes have
	// gone through.
	stdinFails    bool
	stdinWritesOK int

	// refuseRequests answers every request with a refusal; requests records the
	// names of those sent.
	refuseRequests bool
	requests       []string
}

type discardCloser struct{ io.Writer }

func (discardCloser) Close() error { return nil }

// failingStdin accepts ok writes, then fails every one after.
type failingStdin struct{ ok int }

func (f *failingStdin) Write(p []byte) (int, error) {
	if f.ok == 0 {
		return 0, errScripted
	}
	f.ok--
	return len(p), nil
}

func (f *failingStdin) Close() error { return nil }

func (s *scriptedSession) SendRequest(name string, _ bool, _ []byte) (bool, error) {
	s.requests = append(s.requests, name)
	if s.fail == "request" {
		return false, errScripted
	}
	return !s.refuseRequests, nil
}

func (s *scriptedSession) StdinPipe() (io.WriteCloser, error) {
	if s.fail == "stdin" {
		return nil, errScripted
	}
	if s.stdinFails {
		return &failingStdin{ok: s.stdinWritesOK}, nil
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

func TestAFailureWithOnlyStderrIsTheStderr(t *testing.T) {
	p := &proc{}
	p.stderr.WriteString("scp: /x: No such file or directory\n")

	if err := p.failure("copy", nil); err == nil || err.Error() != "scp: copy: scp: /x: No such file or directory" {
		t.Errorf("failure = %v", err)
	}
}

func TestForwardAgentAsksEverySessionBeforeItStarts(t *testing.T) {
	download := &scriptedSession{stdout: "C0644 1 x\nX\x00"}
	copyOnHost := &scriptedSession{}
	c := scripted(download, copyOnHost)
	c.ForwardAgent = true

	if _, err := c.ReadFile(t.Context(), "/x"); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := c.CopyOnHost(t.Context(), "b:/x", "/x"); err != nil {
		t.Fatalf("CopyOnHost: %v", err)
	}
	for name, s := range map[string]*scriptedSession{"download": download, "CopyOnHost": copyOnHost} {
		if !reflect.DeepEqual(s.requests, []string{"auth-agent-req@openssh.com"}) {
			t.Errorf("%s sent %q", name, s.requests)
		}
	}
}

func TestNoAgentIsAskedForUnlessForwardAgentIsSet(t *testing.T) {
	s := &scriptedSession{stdout: "C0644 1 x\nX\x00"}

	if _, err := scripted(s).ReadFile(t.Context(), "/x"); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(s.requests) != 0 {
		t.Errorf("sent %q", s.requests)
	}
}

func TestAgentForwardingThatFailsEndsTheTransfer(t *testing.T) {
	for name, s := range map[string]func() *scriptedSession{
		"refused":       func() *scriptedSession { return &scriptedSession{refuseRequests: true} },
		"request fails": func() *scriptedSession { return &scriptedSession{fail: "request"} },
	} {
		t.Run(name, func(t *testing.T) {
			c := scripted(s(), s())
			c.ForwardAgent = true
			if _, err := c.ReadFile(t.Context(), "/x"); err == nil {
				t.Error("ReadFile: want an error")
			}
			if err := c.CopyOnHost(t.Context(), "b:/x", "/x"); err == nil {
				t.Error("CopyOnHost: want an error")
			}
		})
	}
}
