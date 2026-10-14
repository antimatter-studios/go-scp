package scp

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
)

// Throughput over the in-process SSH server, both ends in this package: a change
// that slows transfers down shows here. Run them with the bench chore.

const benchFileSize = 8 << 20

func BenchmarkUploadLargeFile(b *testing.B) {
	c := newTestServer(b, fileServer).dial(b)
	data := bytes.Repeat([]byte("x"), benchFileSize)
	target := filepath.Join(b.TempDir(), "large")
	b.SetBytes(benchFileSize)
	for b.Loop() {
		if err := c.WriteFile(b.Context(), target, data, 0o644); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDownloadLargeFile(b *testing.B) {
	c := newTestServer(b, fileServer).dial(b)
	dir := b.TempDir()
	writeTree(b, dir, map[string]string{"large": string(bytes.Repeat([]byte("x"), benchFileSize))})
	b.SetBytes(benchFileSize)
	for b.Loop() {
		if _, err := c.Download(b.Context(), filepath.Join(dir, "large"), discard{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCopyToManySmallFiles(b *testing.B) {
	const files, size = 200, 1024
	c := newTestServer(b, fileServer).dial(b)
	c.Recursive = true
	local, remote := b.TempDir(), b.TempDir()
	tree := map[string]string{}
	for i := range files {
		tree[fmt.Sprintf("many/%03d", i)] = string(bytes.Repeat([]byte("x"), size))
	}
	writeTree(b, local, tree)
	b.SetBytes(files * size)
	for b.Loop() {
		if err := c.CopyTo(b.Context(), remote, filepath.Join(local, "many")); err != nil {
			b.Fatal(err)
		}
	}
}

// discard takes bytes and keeps none, without io.Discard's ReaderFrom, so a
// download is read the way it would be into any writer.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
