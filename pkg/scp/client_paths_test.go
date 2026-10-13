package scp

import (
	"errors"
	"strings"
	"testing"
)

// Each case is one way a download can go wrong, scripted at the protocol level.
func TestDownloadPaths(t *testing.T) {
	cases := map[string]struct {
		session *scriptedSession
		want    error
	}{
		"ready cannot be sent":           {&scriptedSession{stdinFails: true}, errScripted},
		"header cannot be accepted":      {&scriptedSession{stdout: "C0644 1 x\nX\x00", stdinFails: true, stdinWritesOK: 1}, errScripted},
		"sender fails after the body":    {&scriptedSession{stdout: "C0644 1 x\nX\x01read error\n"}, nil},
		"control line cut short":         {&scriptedSession{stdout: "C0644 1 x"}, ErrProtocol},
		"time line cut short":            {&scriptedSession{stdout: "T1 0 1 0"}, ErrProtocol},
		"time line malformed":            {&scriptedSession{stdout: "T1 0\n"}, ErrProtocol},
		"time line cannot be accepted":   {&scriptedSession{stdout: "T1 0 1 0\nC0644 1 x\nX\x00", stdinFails: true, stdinWritesOK: 1}, errScripted},
		"a directory instead of a file":  {&scriptedSession{stdout: "D0755 0 d\n"}, ErrProtocol},
		"success where a header belongs": {&scriptedSession{stdout: "\x00"}, ErrProtocol},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := scripted(c.session).ReadFile(t.Context(), "/x")
			if err == nil {
				t.Fatal("want an error")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
			var remote *RemoteError
			if c.want == nil && !errors.As(err, &remote) {
				t.Errorf("err = %v, want the sender's report", err)
			}
		})
	}
}

// Each case is one way an upload can go wrong, scripted at the protocol level.
func TestUploadPaths(t *testing.T) {
	timed := Header{Mode: 0o644, Size: 1, ModTime: modTime}
	plain := Header{Mode: 0o644, Size: 1}
	cases := map[string]struct {
		session  *scriptedSession
		preserve bool
		header   Header
		body     string
		remote   bool
	}{
		"receiver not ready":         {&scriptedSession{stdout: "\x01no\n"}, false, plain, "x", true},
		"times cannot be sent":       {&scriptedSession{stdout: "\x00", stdinFails: true}, true, timed, "x", false},
		"times refused":              {&scriptedSession{stdout: "\x00\x01bad times\n"}, true, timed, "x", true},
		"header cannot be sent":      {&scriptedSession{stdout: "\x00", stdinFails: true}, false, plain, "x", false},
		"header refused":             {&scriptedSession{stdout: "\x00\x01no space\n"}, false, plain, "x", true},
		"body shorter than its size": {&scriptedSession{stdout: "\x00\x00"}, false, plain, "", false},
		"end of body cannot be sent": {&scriptedSession{stdout: "\x00\x00", stdinFails: true, stdinWritesOK: 2}, false, plain, "x", false},
		"receiver fails to store it": {&scriptedSession{stdout: "\x00\x00\x01disk full\n"}, false, plain, "x", true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			client := scripted(c.session)
			client.Preserve = c.preserve
			err := client.Upload(t.Context(), "/x", c.header, strings.NewReader(c.body))
			if err == nil {
				t.Fatal("want an error")
			}
			var remote *RemoteError
			if c.remote != errors.As(err, &remote) {
				t.Errorf("err = %v; a report from the receiver: want %v", err, c.remote)
			}
		})
	}
}
