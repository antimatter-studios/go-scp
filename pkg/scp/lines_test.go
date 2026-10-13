package scp

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

// endlessLine is a peer that sends a line and never ends it: an endless run of one
// byte with no newline. It counts what was read from it.
type endlessLine struct{ read int }

func (e *endlessLine) Read(p []byte) (int, error) {
	if e.read > 1<<20 {
		return 0, errors.New("read a megabyte without the reader giving up")
	}
	for i := range p {
		p[i] = 'a'
	}
	e.read += len(p)
	return len(p), nil
}

func TestAnOverlongControlLineIsRefused(t *testing.T) {
	for name, receive := range map[string]func(io.Reader) error{
		"ReceiveAll": func(in io.Reader) error { return ReceiveAll(in, io.Discard, ReceiveOptions{}, stepSink{}) },
		"Receive":    func(in io.Reader) error { return Receive(in, io.Discard, 0, acceptAll) },
	} {
		t.Run(name, func(t *testing.T) {
			peer := &endlessLine{}
			err := receive(io.MultiReader(strings.NewReader("C0644 1 "), peer))
			if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "longer than") {
				t.Errorf("err = %v, want a protocol error for the long line", err)
			}
			if peer.read > 2*maxLine {
				t.Errorf("read %d bytes of the line, want no more than %d", peer.read, 2*maxLine)
			}
		})
	}
	t.Run("download", func(t *testing.T) {
		_, err := scripted(&scriptedSession{stdout: "C0644 1 " + strings.Repeat("a", 1<<20)}).ReadFile(t.Context(), "/x")
		if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "longer than") {
			t.Errorf("err = %v, want a protocol error for the long line", err)
		}
	})
	t.Run("download time line", func(t *testing.T) {
		_, err := scripted(&scriptedSession{stdout: "T1 " + strings.Repeat("0", 1<<20)}).ReadFile(t.Context(), "/x")
		if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "longer than") {
			t.Errorf("err = %v, want a protocol error for the long line", err)
		}
	})
}

func TestAnOverlongFailureMessageIsCutAndTheStreamGoesOn(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("\x01" + strings.Repeat("m", 3*maxLine) + "\n\x00"))

	var remote *RemoteError
	if err := readStatus(r); !errors.As(err, &remote) || len(remote.Message) != maxLine {
		t.Fatalf("readStatus = %v, want the message cut to %d bytes", err, maxLine)
	}
	if err := readStatus(r); err != nil {
		t.Errorf("the status after the long message: %v, want OK", err)
	}
}
