package scp

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestServerReadOnlyRefusesUploads(t *testing.T) {
	c, served := serving(t, Server{ReadOnly: true})
	writeTree(t, served, map[string]string{"a": "A"})
	local, back := t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"b": "B"})

	if err := c.CopyTo(t.Context(), "/", filepath.Join(local, "b")); err == nil || !strings.Contains(err.Error(), "does not take uploads") {
		t.Errorf("CopyTo = %v, want the upload refused", err)
	}
	if err := c.WriteFile(t.Context(), "/c", []byte("C"), 0o644); err == nil {
		t.Error("WriteFile: want the upload refused")
	}
	if got := readTree(t, served); !reflect.DeepEqual(got, map[string]string{"a": "A"}) {
		t.Errorf("the served directory holds %v, want nothing written", got)
	}
	if err := c.CopyFrom(t.Context(), back, "/a"); err != nil {
		t.Errorf("CopyFrom: %v", err)
	}
}

func TestServerWriteOnlyRefusesDownloads(t *testing.T) {
	c, served := serving(t, Server{WriteOnly: true})
	writeTree(t, served, map[string]string{"a": "A"})

	if err := c.CopyFrom(t.Context(), t.TempDir(), "/a"); err == nil || !strings.Contains(err.Error(), "does not give downloads") {
		t.Errorf("CopyFrom = %v, want the download refused", err)
	}
	if _, err := c.ReadFile(t.Context(), "/a"); err == nil {
		t.Error("ReadFile: want the download refused")
	}
	if err := c.WriteFile(t.Context(), "/b", []byte("B"), 0o644); err != nil {
		t.Errorf("WriteFile: %v", err)
	}
	if got := readTree(t, served); !reflect.DeepEqual(got, map[string]string{"a": "A", "b": "B"}) {
		t.Errorf("the served directory holds %v", got)
	}
}

func TestServerMaxSizeRefusesLargerFiles(t *testing.T) {
	c, served := serving(t, Server{MaxSize: 3, Atomic: true})
	local := t.TempDir()
	writeTree(t, local, map[string]string{"ok": "OK", "big": "too big"})

	err := c.CopyTo(t.Context(), "/", filepath.Join(local, "big"), filepath.Join(local, "ok"))

	files, rest := Failures(err)
	if want := []string{"big"}; !reflect.DeepEqual(paths(files), want) || rest != nil {
		t.Errorf("Failures(%v) = %v, %v; want %v and nothing else", err, paths(files), rest, want)
	}
	if got := readTree(t, served); !reflect.DeepEqual(got, map[string]string{"ok": "OK"}) {
		t.Errorf("the served directory holds %v, want only the file within the limit", got)
	}
}
