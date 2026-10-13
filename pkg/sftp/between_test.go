package sftp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	pkgsftp "github.com/pkg/sftp"
)

func TestCopyBetweenOverSFTP(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeTree(t, first, tree)
	if err := os.Chmod(filepath.Join(first, "project", "README"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := programClient(t, first)
	src.Recursive, src.Preserve = true, true
	dst := NewProgramClient(os.Args[0])

	if err := CopyBetween(t.Context(), src, []string{filepath.Join(first, "project")}, dst, second); err != nil {
		t.Fatalf("CopyBetween: %v", err)
	}
	if got := readTree(t, second); !reflect.DeepEqual(got, tree) {
		t.Fatalf("second host holds %v\nwant %v", got, tree)
	}
	if info, err := os.Stat(filepath.Join(second, "project", "README")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("README: mode %v (%v), want 0600 preserved", info.Mode().Perm(), err)
	}
}

func TestCopyBetweenNamesTheTargetWhenItIsNoDirectory(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeTree(t, first, map[string]string{"a": "A"})
	src := programClient(t, first)

	if err := CopyBetween(t.Context(), src, []string{filepath.Join(first, "a")}, NewProgramClient(os.Args[0]), filepath.Join(second, "b")); err != nil {
		t.Fatalf("CopyBetween: %v", err)
	}
	if got := readTree(t, second); !reflect.DeepEqual(got, map[string]string{"b": "A"}) {
		t.Errorf("second host holds %v", got)
	}
}

func TestCopyBetweenRefusesWhatItCannotDo(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeTree(t, first, map[string]string{"a": "A", "b": "B", "d/x": "X"})
	writeTree(t, second, map[string]string{"file": "F", "into/d": "a file in the way"})
	src := programClient(t, first)
	dst := NewProgramClient(os.Args[0])

	cases := map[string]func() error{
		"no sources": func() error { return CopyBetween(t.Context(), src, nil, dst, second) },
		"several sources onto a file": func() error {
			return CopyBetween(t.Context(), src, []string{filepath.Join(first, "a"), filepath.Join(first, "b")}, dst, filepath.Join(second, "file"))
		},
		"a file where a directory goes": func() error {
			r := *src
			r.Recursive = true
			return CopyBetween(t.Context(), &r, []string{filepath.Join(first, "d")}, dst, filepath.Join(second, "into"))
		},
		"first host unavailable": func() error {
			return CopyBetween(t.Context(), NewProgramClient("true"), []string{"a"}, dst, second)
		},
		"second host unavailable": func() error {
			return CopyBetween(t.Context(), src, []string{filepath.Join(first, "a")}, NewProgramClient("true"), second)
		},
	}
	for name, copyIt := range cases {
		t.Run(name, func(t *testing.T) {
			if err := copyIt(); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestCopyBetweenReportsADirectoryThatCannotBeFinished(t *testing.T) {
	first := t.TempDir()
	if err := os.Mkdir(filepath.Join(first, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := programClient(t, first)
	src.Recursive, src.Preserve = true, true
	f := newFaults()
	f.fail = setstat("/d", true)

	err := CopyBetween(t.Context(), src, []string{filepath.Join(first, "d")}, faultyClient(f), "/d")
	if err == nil || !strings.Contains(err.Error(), "chosen failure") {
		t.Errorf("CopyBetween = %v, want the failed times reported", err)
	}
}

func TestWritingAFileReportsABodyThatEndsShort(t *testing.T) {
	err := faultyClient(newFaults()).with(t.Context(), func(conn *pkgsftp.Client) error {
		return remoteWriter{conn: conn}.writeFile("/f", 0o644, modTime, 10, strings.NewReader("short"))
	})
	if err == nil || !strings.Contains(err.Error(), "only 5 of 10 bytes") {
		t.Errorf("writeFile = %v, want the short body reported", err)
	}
}
