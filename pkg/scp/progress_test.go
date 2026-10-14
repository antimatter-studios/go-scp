package scp

import (
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// progressLog records, for each file, the size it started with and the last count
// of bytes moved.
type progressLog struct {
	mu      sync.Mutex
	started map[string]int64
	last    map[string]int64
}

func newProgressLog() *progressLog {
	return &progressLog{started: map[string]int64{}, last: map[string]int64{}}
}

func (l *progressLog) record(name string, done, size int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if done == 0 {
		l.started[name] = size
	}
	l.last[name] = done
}

// sizes returns the files of a tree as writeTree takes them, with their sizes.
func sizes(files map[string]string) map[string]int64 {
	out := map[string]int64{}
	for name, content := range files {
		out[name] = int64(len(content))
	}
	return out
}

func TestProgressReportsEveryFile(t *testing.T) {
	c := newTestServer(t, fileServer).dial(t)
	c.Recursive = true
	local, remote, back := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, local, tree)
	want := sizes(tree)

	// In order: the copy back needs what the first copy sent.
	for _, step := range []struct {
		name   string
		copyIt func(*Client) error
	}{
		{"CopyTo", func(c *Client) error { return c.CopyTo(t.Context(), remote, filepath.Join(local, "project")) }},
		{"CopyFrom", func(c *Client) error { return c.CopyFrom(t.Context(), back, filepath.Join(remote, "project")) }},
	} {
		name, copyIt := step.name, step.copyIt
		t.Run(name, func(t *testing.T) {
			log := newProgressLog()
			c.Progress = log.record
			if err := copyIt(c); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !reflect.DeepEqual(log.started, want) {
				t.Errorf("started %v\nwant %v", log.started, want)
			}
			if !reflect.DeepEqual(log.last, want) {
				t.Errorf("finished at %v\nwant %v", log.last, want)
			}
		})
	}

	t.Run("one file each way", func(t *testing.T) {
		log := newProgressLog()
		c.Progress = log.record
		target := filepath.Join(remote, "single")
		if err := c.WriteFile(t.Context(), target, []byte("hello"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if _, err := c.ReadFile(t.Context(), target); err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		want := map[string]int64{target: 5, "single": 5}
		if !reflect.DeepEqual(log.last, want) {
			t.Errorf("finished at %v, want %v", log.last, want)
		}
	})
}
