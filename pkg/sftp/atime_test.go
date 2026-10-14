package sftp

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/antimatter-studios/go-scp/pkg/scp"
)

func TestPreserveCarriesAccessTimesOverSFTP(t *testing.T) {
	atime := time.Unix(1_610_000_000, 0) // 2021
	mtime := time.Unix(1_580_000_000, 0) // 2020
	local, remote, back := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"f": "x"})
	if err := os.Chtimes(filepath.Join(local, "f"), atime, mtime); err != nil {
		t.Fatal(err)
	}
	c := programClient(t, remote)
	c.Preserve = true

	accessTime := func(p string) time.Time {
		t.Helper()
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return scp.AccessTime(info)
	}

	if err := c.CopyTo(t.Context(), remote, filepath.Join(local, "f")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	// Checked before the download, which reads the file and may move it on.
	if got := accessTime(filepath.Join(remote, "f")); !got.Equal(atime) {
		t.Errorf("uploaded file's access time = %v, want %v", got, atime)
	}

	// github.com/pkg/sftp's own server reports a file's modification time as its
	// access time too, so the download is checked against OpenSSH's sftp-server,
	// which reports the real one.
	server := openSSHSFTPServer(t)
	down := NewProgramClient(server)
	down.Preserve = true
	if err := down.CopyFrom(t.Context(), back, filepath.Join(remote, "f")); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if got := accessTime(filepath.Join(back, "f")); !got.Equal(atime) {
		t.Errorf("downloaded file's access time = %v, want %v", got, atime)
	}
}

// openSSHSFTPServer returns the path of OpenSSH's sftp-server on this machine,
// and skips the test where there is none.
func openSSHSFTPServer(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/usr/lib/openssh/sftp-server", "/usr/libexec/sftp-server", "/usr/libexec/openssh/sftp-server", "/usr/lib/sftp-server"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("OpenSSH's sftp-server is not installed")
	return ""
}
