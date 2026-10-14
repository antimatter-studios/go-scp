package sftp

import (
	"bytes"
	"fmt"
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

// Many small files, where each file's round trips before its data moves decide the
// speed: one at a time, and eight at once.
func BenchmarkSFTPManySmallFiles(b *testing.B) {
	const count, size = 200, 16 << 10
	local := b.TempDir()
	files := map[string]string{}
	for i := range count {
		files[fmt.Sprintf("many/%03d", i)] = string(bytes.Repeat([]byte("x"), size))
	}
	writeTree(b, local, files)
	for _, parallel := range []int{1, 8} {
		b.Run(fmt.Sprintf("upload/parallel=%d", parallel), func(b *testing.B) {
			remote := b.TempDir()
			c := programClient(b, remote)
			c.Recursive, c.Parallel = true, parallel
			b.SetBytes(count * size)
			for b.Loop() {
				if err := c.CopyTo(b.Context(), remote, filepath.Join(local, "many")); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("download/parallel=%d", parallel), func(b *testing.B) {
			c := programClient(b, local)
			c.Recursive, c.Parallel = true, parallel
			b.SetBytes(count * size)
			for b.Loop() {
				if err := c.CopyFrom(b.Context(), b.TempDir(), filepath.Join(local, "many")); err != nil {
					b.Fatal(err)
				}
			}
		})
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

func TestDownloadsKeepSeveralReadsInFlight(t *testing.T) {
	f := newFaults()
	c := faultyClient(f)
	local := t.TempDir()
	writeTree(t, local, map[string]string{"large": string(bytes.Repeat([]byte("x"), 1<<20))})
	if err := c.CopyTo(t.Context(), "/large", filepath.Join(local, "large")); err != nil {
		t.Fatal(err)
	}
	f.latency = time.Millisecond

	back := t.TempDir()
	if err := c.CopyFrom(t.Context(), back, "/large"); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if got := readTree(t, back)["large"]; len(got) != 1<<20 {
		t.Fatalf("copied %d bytes, want %d", len(got), 1<<20)
	}
	if most := f.mostReading.Load(); most < 2 {
		t.Errorf("at most %d read in flight, want several", most)
	}
}

func TestReadAheadFollowsRequestsAndBufferSize(t *testing.T) {
	if got := (&Client{}).readAhead(); got != defaultRequests*defaultPacket {
		t.Errorf("default read-ahead = %d, want %d", got, defaultRequests*defaultPacket)
	}
	if got := (&Client{Requests: 4, BufferSize: 1024}).readAhead(); got != 4*1024 {
		t.Errorf("read-ahead = %d, want %d", got, 4*1024)
	}
}
