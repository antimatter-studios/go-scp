package sftp

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// progressLog records, for each file, the size it started with, the count of bytes
// it started from, and the last count of bytes moved.
type progressLog struct {
	mu      sync.Mutex
	started map[string]int64
	from    map[string]int64
	last    map[string]int64
}

func newProgressLog() *progressLog {
	return &progressLog{started: map[string]int64{}, from: map[string]int64{}, last: map[string]int64{}}
}

func (l *progressLog) record(name string, done, size int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.started[name]; !ok {
		l.started[name], l.from[name] = size, done
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

func TestProgressReportsEveryFileOverSFTP(t *testing.T) {
	local, remote, back, other := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, local, tree)
	c := programClient(t, remote)
	c.Recursive = true
	want := sizes(tree)

	// In order: each copy needs what the one before it wrote.
	for _, step := range []struct {
		name   string
		copyIt func(*Client) error
	}{
		{"CopyTo", func(c *Client) error { return c.CopyTo(t.Context(), remote, filepath.Join(local, "project")) }},
		{"CopyFrom", func(c *Client) error { return c.CopyFrom(t.Context(), back, filepath.Join(remote, "project")) }},
		{"CopyBetween", func(c *Client) error {
			return CopyBetween(t.Context(), c, []string{filepath.Join(remote, "project")}, c, other)
		}},
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
}

func TestProgressStartsAResumedFileWhereItWas(t *testing.T) {
	local, remote, back := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"up": "0123456789"})
	writeTree(t, remote, map[string]string{"up": "012", "down": "0123456789"})
	writeTree(t, back, map[string]string{"down": "01234"})
	c := programClient(t, remote)
	c.Resume = true
	log := newProgressLog()
	c.Progress = log.record

	if err := c.CopyTo(t.Context(), remote, filepath.Join(local, "up")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "down")); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if want := map[string]int64{"up": 3, "down": 5}; !reflect.DeepEqual(log.from, want) {
		t.Errorf("started from %v, want %v", log.from, want)
	}
	if want := map[string]int64{"up": 10, "down": 10}; !reflect.DeepEqual(log.last, want) {
		t.Errorf("finished at %v, want %v", log.last, want)
	}
	if got, _ := os.ReadFile(filepath.Join(back, "down")); string(got) != "0123456789" {
		t.Errorf("down holds %q", got)
	}
}
