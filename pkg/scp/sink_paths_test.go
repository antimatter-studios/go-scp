package scp

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// stepSink fails the steps given an error, and takes the rest without reading.
type stepSink struct{ file, enter, leave error }

func (s stepSink) File(Header, io.Reader) error { return s.file }
func (s stepSink) EnterDir(Header) error        { return s.enter }
func (s stepSink) LeaveDir() error              { return s.leave }

// Each case is one way receiving a tree can go wrong. in is what the sender says.
func TestReceiveAllPaths(t *testing.T) {
	plain := ReceiveOptions{}
	tree := ReceiveOptions{Recursive: true}

	cases := map[string]struct {
		in   io.Reader
		out  io.Writer
		opts ReceiveOptions
		sink Sink
		want string
	}{
		"ready cannot be sent":              {strings.NewReader(""), &failingStdin{}, plain, stepSink{}, "scripted failure"},
		"time line cannot be accepted":      {strings.NewReader("T1 0 1 0\n"), &failingStdin{ok: 1}, plain, stepSink{}, "scripted failure"},
		"too large and cannot say so":       {strings.NewReader("C0644 5 a\n"), &failingStdin{ok: 1}, ReceiveOptions{MaxSize: 1}, stepSink{}, "scripted failure"},
		"header cannot be accepted":         {strings.NewReader("C0644 1 a\nX\x00"), &failingStdin{ok: 1}, plain, stepSink{}, "scripted failure"},
		"body breaks off":                   {io.MultiReader(strings.NewReader("C0644 5 a\nAB"), iotest.ErrReader(errScripted)), io.Discard, plain, stepSink{}, "reading the body"},
		"sender cannot read the file":       {strings.NewReader("C0644 1 a\nX\x01read error\n"), io.Discard, plain, stepSink{}, "read error"},
		"file failure cannot be sent":       {strings.NewReader("C0644 1 a\nX\x00"), &failingStdin{ok: 2}, plain, stepSink{file: errScripted}, "scripted failure"},
		"directory cannot be entered":       {strings.NewReader("D0755 0 d\n"), io.Discard, tree, stepSink{enter: errScripted}, "scripted failure"},
		"directory cannot be accepted":      {strings.NewReader("D0755 0 d\n"), &failingStdin{ok: 1}, tree, stepSink{}, "scripted failure"},
		"directory cannot be left":          {strings.NewReader("D0755 0 d\nE\n"), io.Discard, tree, stepSink{leave: errScripted}, "scripted failure"},
		"end of directory cannot be agreed": {strings.NewReader("D0755 0 d\nE\n"), &failingStdin{ok: 2}, tree, stepSink{}, "scripted failure"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := ReceiveAll(c.in, c.out, c.opts, c.sink)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("ReceiveAll = %v, want an error containing %q", err, c.want)
			}
		})
	}
}
