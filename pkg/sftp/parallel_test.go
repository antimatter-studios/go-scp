package sftp

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/antimatter-studios/go-scp/pkg/scp"
	pkgsftp "github.com/pkg/sftp"
)

func TestParallelCopiesSeveralFilesAtOnce(t *testing.T) {
	files := map[string]string{}
	for i := range 8 {
		files[fmt.Sprintf("d/f%d", i)] = strings.Repeat("x", 100+i)
		files[fmt.Sprintf("d/sub/g%d", i)] = strings.Repeat("y", 100+i)
	}
	local := t.TempDir()
	writeTree(t, local, files)

	for _, parallel := range []int{1, 4} {
		t.Run(fmt.Sprint(parallel), func(t *testing.T) {
			f := newFaults()
			c := faultyClient(f)
			c.Recursive, c.Parallel = true, parallel
			f.latency = 20 * time.Millisecond
			// Two files fail inside, which the copy reports in the listing's order
			// whatever order they finish in.
			f.set(func(r *pkgsftp.Request) bool {
				return r.Method == "Put" && (r.Filepath == "/d/f6" || r.Filepath == "/d/f1")
			})
			var calling atomic.Bool
			c.Progress = func(string, int64, int64) {
				if calling.Swap(true) {
					t.Error("Progress called from two files at once")
				}
				time.Sleep(time.Millisecond)
				calling.Store(false)
			}

			err := c.CopyTo(t.Context(), "/", filepath.Join(local, "d"))
			got, rest := scp.Failures(err)
			if want := []string{"d/f1", "d/f6"}; !reflect.DeepEqual(paths(got), want) || rest != nil {
				t.Errorf("CopyTo failures %v, %v; want %v in that order", paths(got), rest, want)
			}
			f.set(nil)
			back := t.TempDir()
			if err := c.CopyFrom(t.Context(), back, "/d"); err != nil {
				t.Fatalf("CopyFrom: %v", err)
			}
			if err := CopyBetween(t.Context(), c, []string{"/d"}, c, "/e"); err != nil {
				t.Fatalf("CopyBetween: %v", err)
			}

			want := map[string]string{}
			for name, content := range files {
				if name != "d/f1" && name != "d/f6" {
					want[name] = content
				}
			}
			if got := readTree(t, back); !reflect.DeepEqual(got, want) {
				t.Errorf("copied back %v\nwant %v", got, want)
			}
			reads, writes := f.mostReading.Load(), f.mostWriting.Load()
			if parallel == 1 && (reads != 1 || writes != 1) {
				t.Errorf("one at a time, yet %d reads and %d writes were in flight at once", reads, writes)
			}
			if parallel > 1 && (reads < 2 || writes < 2 || reads > int32(parallel) || writes > int32(parallel)) {
				t.Errorf("at most %d reads and %d writes in flight at once, want several and no more than %d", reads, writes, parallel)
			}
		})
	}
}
