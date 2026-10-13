package scp

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClock lets the limiter's arithmetic be checked without waiting.
type fakeClock struct {
	t     time.Time
	slept time.Duration
}

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) sleep(d time.Duration) {
	c.slept += d
	c.t = c.t.Add(d)
}

func pacedBy(rate int64) (*limiter, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1700000000, 0)}
	l := newLimiter(rate)
	l.now, l.sleep = clock.now, clock.sleep
	return l, clock
}

func TestBandwidthLimitPacesBytesToTheRate(t *testing.T) {
	l, clock := pacedBy(1000) // 1000 bytes a second
	var out bytes.Buffer

	n, err := limitedWriter{w: &out, l: l}.Write(make([]byte, 5000))

	if err != nil || n != 5000 || out.Len() != 5000 {
		t.Fatalf("Write = (%d, %v), wrote %d", n, err, out.Len())
	}
	if clock.slept != 5*time.Second {
		t.Errorf("slept %v for 5000 bytes at 1000/s, want 5s", clock.slept)
	}
}

func TestBandwidthLimitSharesOnePaceBetweenDirections(t *testing.T) {
	l, clock := pacedBy(1000)
	var out bytes.Buffer
	in := limitedReader{r: strings.NewReader(strings.Repeat("x", 3000)), l: l}

	limitedWriter{w: &out, l: l}.Write(make([]byte, 2000))
	io.Copy(io.Discard, in)

	if clock.slept != 5*time.Second {
		t.Errorf("slept %v for 5000 bytes both ways at 1000/s, want 5s", clock.slept)
	}
}

func TestBandwidthLimitMovesAtMostATenthOfASecondAtATime(t *testing.T) {
	l, _ := pacedBy(1000)
	var sizes []int
	w := limitedWriter{w: writerFunc(func(p []byte) (int, error) {
		sizes = append(sizes, len(p))
		return len(p), nil
	}), l: l}

	w.Write(make([]byte, 250))

	if len(sizes) != 3 || sizes[0] != 100 || sizes[2] != 50 {
		t.Errorf("chunks = %v, want 100 100 50", sizes)
	}
	r := limitedReader{r: strings.NewReader(strings.Repeat("x", 500)), l: l}
	if n, _ := r.Read(make([]byte, 500)); n != 100 {
		t.Errorf("a read returned %d bytes, want at most 100", n)
	}
}

func TestBandwidthLimitBelowTenBytesASecondStillMoves(t *testing.T) {
	l, clock := pacedBy(4)
	var out bytes.Buffer

	limitedWriter{w: &out, l: l}.Write([]byte("abcd"))

	if out.String() != "abcd" || clock.slept != time.Second {
		t.Errorf("wrote %q, slept %v; want abcd and 1s", out.String(), clock.slept)
	}
}

func TestBandwidthLimitPassesWriteErrorsOn(t *testing.T) {
	l, _ := pacedBy(1000)
	failing := writerFunc(func(p []byte) (int, error) { return 0, io.ErrClosedPipe })

	if _, err := (limitedWriter{w: failing, l: l}).Write([]byte("x")); err != io.ErrClosedPipe {
		t.Fatalf("err = %v", err)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// Real transfers, timed: a limit must slow a transfer to the rate, and not much
// beyond it.
func TestBandwidthLimitHoldsATransferToTheRate(t *testing.T) {
	const size, rate = 40_000, 100_000 // 0.4s at the limit
	content := bytes.Repeat([]byte("0123456789"), size/10)

	for name, transfer := range map[string]func(c *Client, name string) error{
		"upload": func(c *Client, name string) error {
			return c.WriteFile(t.Context(), name, content, 0o644)
		},
		"download": func(c *Client, name string) error {
			if err := os.WriteFile(name, content, 0o644); err != nil {
				return err
			}
			_, err := c.ReadFile(t.Context(), name)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := newTestServer(t, fileServer).dial(t)
			c.Limit = rate
			start := time.Now()

			if err := transfer(c, filepath.Join(t.TempDir(), "paced")); err != nil {
				t.Fatalf("transfer: %v", err)
			}

			elapsed := time.Since(start)
			if elapsed < 350*time.Millisecond || elapsed > 3*time.Second {
				t.Errorf("%d bytes at %d/s took %v, want about 0.4s", size, rate, elapsed)
			}
		})
	}
}

func TestBandwidthLimitIsOffByDefault(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)
	start := time.Now()

	if err := c.WriteFile(t.Context(), filepath.Join(t.TempDir(), "fast"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("an unlimited 1 MiB transfer took %v", elapsed)
	}
}
