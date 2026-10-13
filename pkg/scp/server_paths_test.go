package scp

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func acceptAll(_ Header, body io.Reader) error {
	_, err := io.ReadAll(body)
	return err
}

func refuseAll(Header, io.Reader) error { return errScripted }

// Each case is one way a single-file receive can go wrong.
func TestReceivePaths(t *testing.T) {
	cases := map[string]struct {
		in     io.Reader
		out    io.Writer
		accept func(Header, io.Reader) error
		want   error
	}{
		"ready cannot be sent":             {strings.NewReader("C0644 1 a\nX\x00"), &failingStdin{}, acceptAll, errScripted},
		"header cannot be accepted":        {strings.NewReader("C0644 1 a\nX\x00"), &failingStdin{ok: 1}, acceptAll, errScripted},
		"time line cannot be accepted":     {strings.NewReader("T1 0 1 0\nC0644 1 a\nX\x00"), &failingStdin{ok: 1}, acceptAll, errScripted},
		"nothing sent":                     {strings.NewReader(""), io.Discard, acceptAll, ErrProtocol},
		"body breaks off":                  {io.MultiReader(strings.NewReader("C0644 5 a\nAB"), iotest.ErrReader(errScripted)), io.Discard, func(Header, io.Reader) error { return nil }, errScripted},
		"no status after a refused body":   {strings.NewReader("C0644 1 a\nX"), io.Discard, refuseAll, errScripted},
		"no status after an accepted body": {strings.NewReader("C0644 1 a\nX"), io.Discard, acceptAll, ErrProtocol},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Receive(c.in, c.out, 0, c.accept); !errors.Is(err, c.want) {
				t.Errorf("Receive = %v, want %v", err, c.want)
			}
		})
	}
}

func TestReceiveReturnsTheSendersFailureAfterTheBody(t *testing.T) {
	err := Receive(strings.NewReader("C0644 1 a\nX\x01read error\n"), io.Discard, 0, acceptAll)

	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Message != "read error" {
		t.Errorf("Receive = %v, want the sender's report", err)
	}
}

func TestRejectionErrorIsTheUnderlyingError(t *testing.T) {
	r := &Rejection{Message: "shown to the peer", Err: errScripted}

	if r.Error() != errScripted.Error() {
		t.Errorf("Error = %q", r.Error())
	}
}

// Each case is one way a single-file send can go wrong.
func TestSendPaths(t *testing.T) {
	timed := Header{Mode: 0o644, Size: 1, Name: "a", ModTime: modTime}
	plain := Header{Mode: 0o644, Size: 1, Name: "a"}
	cases := map[string]struct {
		in     string
		out    io.Writer
		header Header
		body   io.Reader
		want   error
	}{
		"peer never ready":           {"", io.Discard, plain, strings.NewReader("x"), ErrProtocol},
		"times cannot be sent":       {"\x00", &failingStdin{}, timed, strings.NewReader("x"), errScripted},
		"times refused":              {"\x00\x01bad\n", io.Discard, timed, strings.NewReader("x"), nil},
		"header cannot be sent":      {"\x00", &failingStdin{}, plain, strings.NewReader("x"), errScripted},
		"padding cannot be sent":     {"\x00\x00", &failingStdin{ok: 1}, plain, iotest.ErrReader(errScripted), errScripted},
		"end of body cannot be sent": {"\x00\x00", &failingStdin{ok: 2}, plain, strings.NewReader("x"), errScripted},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := Send(strings.NewReader(c.in), c.out, c.header, c.body)
			if err == nil {
				t.Fatal("want an error")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Errorf("Send = %v, want %v", err, c.want)
			}
		})
	}
}

func TestSendFailureNeedsThePeerReady(t *testing.T) {
	for name, in := range map[string]string{"no answer": "", "not a status byte": "\x05"} {
		t.Run(name, func(t *testing.T) {
			if err := SendFailure(strings.NewReader(in), io.Discard, "gone"); !errors.Is(err, ErrProtocol) {
				t.Errorf("SendFailure = %v, want ErrProtocol", err)
			}
		})
	}
}
