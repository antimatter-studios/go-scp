package scp

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

// Each case is one way passing an entry on to the receiving host can go wrong.
// in is what the receiving host says.
func TestForwardSinkPaths(t *testing.T) {
	timed := Header{Mode: 0o644, Size: 1, Name: "a", ModTime: modTime}
	plain := Header{Mode: 0o644, Size: 1, Name: "a"}
	dir := Header{Mode: 0o755, Name: "d"}
	timedDir := Header{Mode: 0o755, Name: "d", ModTime: modTime}

	cases := map[string]struct {
		in   string
		out  io.Writer
		call func(Sink) error
	}{
		"file times cannot be sent":       {"", &failingStdin{}, func(s Sink) error { return s.File(timed, strings.NewReader("x")) }},
		"file header cannot be sent":      {"", &failingStdin{}, func(s Sink) error { return s.File(plain, strings.NewReader("x")) }},
		"file header refused":             {"\x01no\n", io.Discard, func(s Sink) error { return s.File(plain, strings.NewReader("x")) }},
		"body shorter than its size":      {"\x00", io.Discard, func(s Sink) error { return s.File(plain, strings.NewReader("")) }},
		"end of body cannot be sent":      {"\x00", &failingStdin{ok: 2}, func(s Sink) error { return s.File(plain, strings.NewReader("x")) }},
		"directory times cannot be sent":  {"", &failingStdin{}, func(s Sink) error { return s.EnterDir(timedDir) }},
		"directory line cannot be sent":   {"", &failingStdin{}, func(s Sink) error { return s.EnterDir(dir) }},
		"end of directory cannot be sent": {"", &failingStdin{}, func(s Sink) error { return s.LeaveDir() }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			sink := forwardSink{r: bufio.NewReader(strings.NewReader(c.in)), w: c.out}
			if err := c.call(sink); err == nil {
				t.Error("want an error")
			}
		})
	}
}
