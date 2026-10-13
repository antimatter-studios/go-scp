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

	"golang.org/x/crypto/ssh"
)

func dialTestHost(t *testing.T) *ssh.Client {
	t.Helper()
	addr, keyFile := os.Getenv("GOSCP_HOST_A"), os.Getenv("GOSCP_KEY")
	if addr == "" || keyFile == "" {
		t.Fatal("GOSCP_HOST_A and GOSCP_KEY are not set: run the integration tests with scripts/integration.sh")
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
	conn := dialTestHost(t)
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
	conn := dialTestHost(t)
	a := NewAuto(conn)
	// Make the classic client fail, so only a copy over SFTP can succeed.
	a.SCP.Program = "false"
	local := t.TempDir()
	writeTree(t, local, map[string]string{"a": "A"})

	if err := a.CopyTo(t.Context(), remoteName(t, conn), filepath.Join(local, "a")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
}
