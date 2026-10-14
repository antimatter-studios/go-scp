package sftp

import (
	"io"
	"sync"
	"time"
)

// pacer holds a copy to a rate, in bytes per second, across every file in it.
// A nil pacer does not pace.
type pacer struct {
	rate int64

	mu    sync.Mutex
	start time.Time
	moved int64
}

// newPacer returns a pacer for rate bytes per second, or nil when rate is not
// positive.
func newPacer(rate int64) *pacer {
	if rate <= 0 {
		return nil
	}
	return &pacer{rate: rate}
}

// chunk is how many bytes move between pauses: a tenth of a second's worth, so the
// pace is smooth rather than a burst followed by a long wait.
func (p *pacer) chunk() int {
	if c := p.rate / 10; c > 0 {
		return int(c)
	}
	return 1
}

// wait records n more bytes and waits until the copy is no further ahead than the
// rate allows.
func (p *pacer) wait(n int) {
	p.mu.Lock()
	if p.start.IsZero() {
		p.start = time.Now()
	}
	p.moved += int64(n)
	due := p.start.Add(time.Duration(float64(p.moved) / float64(p.rate) * float64(time.Second)))
	p.mu.Unlock()
	if ahead := time.Until(due); ahead > 0 {
		time.Sleep(ahead)
	}
}

// reader returns r read at p's pace, or r itself when p is nil.
func (p *pacer) reader(r io.Reader) io.Reader {
	if p == nil {
		return r
	}
	return pacedReader{r, p}
}

type pacedReader struct {
	r io.Reader
	p *pacer
}

func (pr pacedReader) Read(b []byte) (int, error) {
	if len(b) > pr.p.chunk() {
		b = b[:pr.p.chunk()]
	}
	n, err := pr.r.Read(b)
	pr.p.wait(n)
	return n, err
}
