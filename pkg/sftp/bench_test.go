package sftp

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

// Throughput, a change that slows copies down shows here: plain copies against the
// test binary serving the real filesystem, and copies over latency against the
// in-memory server, which can delay its answers. Run them with the bench chore.

const benchFileSize = 8 << 20

// benchFile writes a large file in a new directory and returns its path.
func benchFile(b *testing.B) string {
	b.Helper()
	dir := b.TempDir()
	writeTree(b, dir, map[string]string{"large": string(bytes.Repeat([]byte("x"), benchFileSize))})
	return filepath.Join(dir, "large")
}

func BenchmarkSFTPUploadLargeFile(b *testing.B) {
	remote := b.TempDir()
	c := programClient(b, remote)
	large := benchFile(b)
	b.SetBytes(benchFileSize)
	for b.Loop() {
		if err := c.CopyTo(b.Context(), remote, large); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSFTPDownloadLargeFile(b *testing.B) {
	large := benchFile(b)
	c := programClient(b, filepath.Dir(large))
	b.SetBytes(benchFileSize)
	for b.Loop() {
		if err := c.CopyFrom(b.Context(), b.TempDir(), large); err != nil {
			b.Fatal(err)
		}
	}
}

// Each read waits a millisecond, as over a link with that much latency: here the
// number of reads in flight decides the speed.
func BenchmarkSFTPDownloadOverLatency(b *testing.B) {
	c := slowServer(b, time.Millisecond)
	b.SetBytes(benchFileSize)
	for b.Loop() {
		if err := c.CopyFrom(b.Context(), b.TempDir(), "/large"); err != nil {
			b.Fatal(err)
		}
	}
}

// slowServer returns a client for an in-memory server holding /large, whose every
// read is answered after latency.
func slowServer(b *testing.B, latency time.Duration) *Client {
	b.Helper()
	f := newFaults()
	c := faultyClient(f)
	if err := c.CopyTo(b.Context(), "/large", benchFile(b)); err != nil {
		b.Fatal(err)
	}
	f.latency = latency
	return c
}
