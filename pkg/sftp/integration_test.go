//go:build integration

package sftp

// Integration tests against the real SSH hosts in test/network, started by
// scripts/integration.sh: OpenSSH's own sshd and sftp-server.

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/go-scp/pkg/scp"
	"golang.org/x/crypto/ssh"
)

// dialTestHost connects to the test host whose address is in the environment
// variable addrEnv, GOSCP_HOST_A or GOSCP_HOST_B.
func dialTestHost(t *testing.T, addrEnv string) *ssh.Client {
	t.Helper()
	addr, keyFile := os.Getenv(addrEnv), os.Getenv("GOSCP_KEY")
	if addr == "" || keyFile == "" {
		t.Fatalf("%s and GOSCP_KEY are not set: run the integration tests with scripts/integration.sh", addrEnv)
	}
	pem, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "tester",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // throwaway containers with fresh host keys
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// remoteName returns a name in the login user's home for one test, removed
// afterwards.
func remoteName(t *testing.T, conn *ssh.Client) string {
	t.Helper()
	suffix := make([]byte, 4)
	rand.Read(suffix)
	name := strings.ReplaceAll(t.Name(), "/", "_") + "-" + hex.EncodeToString(suffix)
	t.Cleanup(func() {
		if s, err := conn.NewSession(); err == nil {
			s.Run("rm -rf -- '" + name + "'")
			s.Close()
		}
	})
	return name
}

func TestIntegrationSFTPCopiesATreeBothWays(t *testing.T) {
	conn := dialTestHost(t, "GOSCP_HOST_A")
	c := NewClient(conn)
	c.Recursive, c.Preserve = true, true
	local, back := t.TempDir(), t.TempDir()
	writeTree(t, local, tree)
	remote := remoteName(t, conn)

	// Relative to the login directory, where sftp-server starts.
	if err := c.CopyTo(t.Context(), remote, filepath.Join(local, "project")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if err := c.CopyFrom(t.Context(), back, "~/"+remote); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	want := map[string]string{}
	for name, content := range tree {
		want[remote+strings.TrimPrefix(name, "project")] = content
	}
	if got := readTree(t, back); !reflect.DeepEqual(got, want) {
		t.Errorf("copied back %v\nwant %v", got, want)
	}
}

func TestIntegrationAutoUsesSFTPOnOpenSSH(t *testing.T) {
	conn := dialTestHost(t, "GOSCP_HOST_A")
	a := NewAuto(conn)
	// Make the classic client fail, so only a copy over SFTP can succeed.
	a.SCP.Program = "false"
	local := t.TempDir()
	writeTree(t, local, map[string]string{"a": "A"})

	if err := a.CopyTo(t.Context(), remoteName(t, conn), filepath.Join(local, "a")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
}

func TestIntegrationSFTPCopyBetweenHosts(t *testing.T) {
	connA, connB := dialTestHost(t, "GOSCP_HOST_A"), dialTestHost(t, "GOSCP_HOST_B")
	onA, onB := NewClient(connA), NewClient(connB)
	onA.Recursive, onB.Recursive = true, true
	local, back := t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"d/a": "A", "d/sub/b": "B"})
	first, second := remoteName(t, connA), remoteName(t, connB)

	if err := onA.CopyTo(t.Context(), first, filepath.Join(local, "d")); err != nil {
		t.Fatalf("CopyTo host-a: %v", err)
	}
	if err := CopyBetween(t.Context(), onA, []string{first}, onB, second); err != nil {
		t.Fatalf("CopyBetween: %v", err)
	}
	if err := onB.CopyFrom(t.Context(), back, second); err != nil {
		t.Fatalf("CopyFrom host-b: %v", err)
	}
	want := map[string]string{second + "/a": "A", second + "/sub/b": "B"}
	if got := readTree(t, back); !reflect.DeepEqual(got, want) {
		t.Errorf("copied back %v\nwant %v", got, want)
	}
}

func TestIntegrationSFTPPreserveCarriesAccessTimes(t *testing.T) {
	atime := time.Unix(1_610_000_000, 0) // 2021
	mtime := time.Unix(1_580_000_000, 0) // 2020
	conn := dialTestHost(t, "GOSCP_HOST_A")
	c := NewClient(conn)
	c.Preserve = true
	local, back := t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"f": "x"})
	if err := os.Chtimes(filepath.Join(local, "f"), atime, mtime); err != nil {
		t.Fatal(err)
	}
	remote := remoteName(t, conn)

	if err := c.CopyTo(t.Context(), remote, filepath.Join(local, "f")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if err := c.CopyFrom(t.Context(), back, remote); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	info, err := os.Stat(filepath.Join(back, remote))
	if err != nil {
		t.Fatal(err)
	}
	if got := scp.AccessTime(info); !got.Equal(atime) || !info.ModTime().Equal(mtime) {
		t.Errorf("times after a round trip: access %v, modified %v; want %v and %v", got, info.ModTime(), atime, mtime)
	}
}
