package scp

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// eachPair runs a test with two connected clients, a source and a destination,
// against both stand-ins for a remote end.
func eachPair(t *testing.T, test func(t *testing.T, src, dst *Client)) {
	t.Run("go servers", func(t *testing.T) {
		test(t, newTestServer(t, fileServer).dial(t), newTestServer(t, fileServer).dial(t))
	})
	t.Run("openssh scp", func(t *testing.T) {
		scp := requireOpenSSHScp(t)
		test(t, newTestServer(t, openSSHServer(scp)).dial(t), newTestServer(t, openSSHServer(scp)).dial(t))
	})
}

func TestCopyBetweenCopiesATree(t *testing.T) {
	eachPair(t, func(t *testing.T, src, dst *Client) {
		src.Recursive = true
		from := t.TempDir()
		writeTree(t, from, tree)
		to := t.TempDir()

		if err := CopyBetween(t.Context(), src, []string{filepath.Join(from, "project")}, dst, to); err != nil {
			t.Fatalf("CopyBetween: %v", err)
		}
		if got := readTree(t, to); !reflect.DeepEqual(got, tree) {
			t.Errorf("destination holds %v\nwant %v", got, tree)
		}
	})
}

func TestCopyBetweenSeveralFilesAndAWildcard(t *testing.T) {
	eachPair(t, func(t *testing.T, src, dst *Client) {
		src.Glob = true
		from := t.TempDir()
		writeTree(t, from, map[string]string{"a.txt": "A", "b.txt": "B", "c.log": "C", "notes": "N"})
		to := t.TempDir()

		err := CopyBetween(t.Context(), src, []string{filepath.Join(from, "*.txt"), filepath.Join(from, "notes")}, dst, to)

		if err != nil {
			t.Fatalf("CopyBetween: %v", err)
		}
		want := map[string]string{"a.txt": "A", "b.txt": "B", "notes": "N"}
		if got := readTree(t, to); !reflect.DeepEqual(got, want) {
			t.Errorf("destination holds %v, want %v", got, want)
		}
	})
}

func TestCopyBetweenReportsAMissingSource(t *testing.T) {
	eachPair(t, func(t *testing.T, src, dst *Client) {
		err := CopyBetween(t.Context(), src, []string{filepath.Join(t.TempDir(), "absent")}, dst, t.TempDir())

		if err == nil || !strings.Contains(err.Error(), "No such file") {
			t.Fatalf("err = %v, want the source's report", err)
		}
	})
}

func TestCopyBetweenNeedsSourcesAndALiveContext(t *testing.T) {
	src, dst := newTestServer(t, fileServer).dial(t), newTestServer(t, fileServer).dial(t)

	if err := CopyBetween(t.Context(), src, nil, dst, t.TempDir()); err == nil {
		t.Error("no sources must be an error")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := CopyBetween(ctx, src, []string{"/x"}, dst, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestCopyBetweenIsPacedByTheSourcesLimit(t *testing.T) {
	src, dst := newTestServer(t, fileServer).dial(t), newTestServer(t, fileServer).dial(t)
	src.Limit = 100_000
	from := filepath.Join(t.TempDir(), "paced")
	writeTree(t, filepath.Dir(from), map[string]string{"paced": strings.Repeat("0123456789", 4_000)})
	start := time.Now()

	if err := CopyBetween(t.Context(), src, []string{from}, dst, t.TempDir()); err != nil {
		t.Fatalf("CopyBetween: %v", err)
	}

	if elapsed := time.Since(start); elapsed < 350*time.Millisecond || elapsed > 3*time.Second {
		t.Errorf("40000 bytes at 100000/s took %v, want about 0.4s", elapsed)
	}
}
