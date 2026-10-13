package scp

import (
	"io"
	"sync"
	"time"
)

// limiter paces the bytes of one transfer to a rate. Both directions share it, so
// the limit applies to the transfer as a whole, as scp's -l does.
type limiter struct {
	rate int64 // bytes per second

	mu    sync.Mutex
	start time.Time
	moved int64

	now   func() time.Time
	sleep func(time.Duration)
}

func newLimiter(rate int64) *limiter {
	return &limiter{rate: rate, now: time.Now, sleep: time.Sleep}
}

// chunk is how many bytes move between pauses: a tenth of a second's worth, so the
// pace is smooth rather than a burst followed by a long wait.
func (l *limiter) chunk() int {
	if c := l.rate / 10; c > 0 {
		return int(c)
	}
	return 1
}

// wait records n more bytes and waits until the transfer is no further ahead than
// the rate allows.
func (l *limiter) wait(n int) {
	if n <= 0 {
		return
	}
	l.mu.Lock()
	if l.start.IsZero() {
		l.start = l.now()
	}
	l.moved += int64(n)
	due := l.start.Add(time.Duration(float64(l.moved) / float64(l.rate) * float64(time.Second)))
	ahead := due.Sub(l.now())
	l.mu.Unlock()
	if ahead > 0 {
		l.sleep(ahead)
	}
}

// limitedWriter writes through a limiter, a chunk at a time.
type limitedWriter struct {
	w io.Writer
	l *limiter
}

func (lw limitedWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := min(len(p), lw.l.chunk())
		m, err := lw.w.Write(p[:n])
		written += m
		lw.l.wait(m)
		if err != nil {
			return written, err
		}
		p = p[m:]
	}
	return written, nil
}

// limitedReader reads through a limiter, at most a chunk at a time.
type limitedReader struct {
	r io.Reader
	l *limiter
}

func (lr limitedReader) Read(p []byte) (int, error) {
	if len(p) > lr.l.chunk() {
		p = p[:lr.l.chunk()]
	}
	n, err := lr.r.Read(p)
	lr.l.wait(n)
	return n, err
}
