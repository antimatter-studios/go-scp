//go:build integration

package scp

// Integration tests against real SSH hosts: the containers in test/network, started
// by scripts/integration.sh. Every remote command goes through the host's sshd and
// the user's login shell, as it does in production.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func integrationEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set: run the integration tests with scripts/integration.sh", name)
	}
	return v
}

// testKey returns the test network's private key, which every test user logs in
// with.
func testKey(t *testing.T) any {
	t.Helper()
	pem, err := os.ReadFile(integrationEnv(t, "GOSCP_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.ParseRawPrivateKey(pem)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// dialHost connects to one of the test hosts as the tester user.
func dialHost(t *testing.T, addrEnv string) *ssh.Client {
	t.Helper()
	return dialHostAs(t, addrEnv, "tester")
}

// dialHostAs connects to one of the test hosts as user.
func dialHostAs(t *testing.T, addrEnv, user string) *ssh.Client {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := ssh.Dial("tcp", integrationEnv(t, addrEnv), &ssh.ClientConfig{
		User:            user,
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

// runRemote runs a command on the host through its login shell and returns stdout.
func runRemote(t *testing.T, conn *ssh.Client, cmd string) string {
	t.Helper()
	sess, err := conn.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdout, sess.Stderr = &stdout, &stderr
	if err := sess.Run(cmd); err != nil {
		t.Fatalf("%s: %v: %s", cmd, err, stderr.String())
	}
	return stdout.String()
}

// remoteDir makes an empty directory on the host for one test, removed afterwards.
func remoteDir(t *testing.T, conn *ssh.Client) string {
	t.Helper()
	suffix := make([]byte, 4)
	rand.Read(suffix)
	dir := "/home/" + conn.User() + "/" + strings.ReplaceAll(t.Name(), "/", "_") + "-" + hex.EncodeToString(suffix)
	runRemote(t, conn, "mkdir -p "+Quote(dir))
	t.Cleanup(func() { runRemote(t, conn, "rm -rf "+Quote(dir)) })
	return dir
}

func TestIntegrationFileRoundTripsThroughALoginShell(t *testing.T) {
	conn := dialHost(t, "GOSCP_HOST_A")
	c := NewClient(conn)
	name := remoteDir(t, conn) + "/it's got spaces, $HOME and *.txt"
	content := []byte("line one\n\x00binary\xff\n")

	if err := c.WriteFile(t.Context(), name, content, 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := c.ReadFile(t.Context(), name)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("ReadFile = (%q, %v)", got, err)
	}
	if mode := strings.TrimSpace(runRemote(t, conn, "stat -c %a "+Quote(name))); mode != "640" {
		t.Errorf("remote mode = %s, want 640", mode)
	}
}

func TestIntegrationMissingFileIsTheRemotesReport(t *testing.T) {
	conn := dialHost(t, "GOSCP_HOST_A")

	_, err := NewClient(conn).ReadFile(t.Context(), remoteDir(t, conn)+"/absent")

	var remote *RemoteError
	if !errors.As(err, &remote) || !strings.Contains(remote.Message, "No such file or directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestIntegrationTreeRoundTrips(t *testing.T) {
	conn := dialHost(t, "GOSCP_HOST_A")
	c := NewClient(conn)
	c.Recursive = true
	local := t.TempDir()
	writeTree(t, local, tree)
	remote := remoteDir(t, conn)
	back := t.TempDir()

	if err := c.CopyTo(t.Context(), remote, filepath.Join(local, "project")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if err := c.CopyFrom(t.Context(), back, remote+"/project"); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if got := readTree(t, back); !reflect.DeepEqual(got, tree) {
		t.Errorf("round trip = %v\nwant %v", got, tree)
	}
}

func TestIntegrationPreserveSetsRemoteTimes(t *testing.T) {
	conn := dialHost(t, "GOSCP_HOST_A")
	c := NewClient(conn)
	c.Preserve = true
	name := remoteDir(t, conn) + "/kept"

	if err := c.Upload(t.Context(), name, Header{Mode: 0o600, Size: 2, ModTime: modTime, AccessTime: accessTime}, strings.NewReader("hi")); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if mtime := strings.TrimSpace(runRemote(t, conn, "stat -c %Y "+Quote(name))); mtime != "1700000000" {
		t.Errorf("remote mtime = %s, want 1700000000", mtime)
	}

	var buf bytes.Buffer
	h, err := c.Download(t.Context(), name, &buf)
	if err != nil || h.ModTime.Unix() != modTime.Unix() {
		t.Fatalf("Download = (%+v, %v)", h, err)
	}
}

func TestIntegrationRemoteShellExpandsWildcards(t *testing.T) {
	conn := dialHost(t, "GOSCP_HOST_A")
	c := NewClient(conn)
	remote := remoteDir(t, conn)
	for name, content := range map[string]string{"a.txt": "A", "b.txt": "B", "c.log": "C"} {
		if err := c.WriteFile(t.Context(), remote+"/"+name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c.Glob = true
	local := t.TempDir()

	if err := c.CopyFrom(t.Context(), local, remote+"/*.txt"); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if got := readTree(t, local); !reflect.DeepEqual(got, map[string]string{"a.txt": "A", "b.txt": "B"}) {
		t.Errorf("local holds %v", got)
	}
}

func TestIntegrationSudoScpWritesWhereTheUserCannot(t *testing.T) {
	conn := dialHost(t, "GOSCP_HOST_A")
	c := NewClient(conn)
	name := "/srv/root-only/" + strings.ReplaceAll(t.Name(), "/", "_")

	if err := c.WriteFile(t.Context(), name, []byte("x"), 0o644); err == nil {
		t.Fatal("writing into a root-owned directory as the login user succeeded")
	}

	c.Program = "sudo -n scp"
	if err := c.WriteFile(t.Context(), name, []byte("as root"), 0o644); err != nil {
		t.Fatalf("WriteFile with sudo: %v", err)
	}
	if owner := strings.TrimSpace(runRemote(t, conn, "stat -c %U "+Quote(name))); owner != "root" {
		t.Errorf("owner = %s, want root", owner)
	}
}

func TestIntegrationHostsReachEachOtherByName(t *testing.T) {
	conn := dialHost(t, "GOSCP_HOST_A")
	dialHost(t, "GOSCP_HOST_B")

	if got := strings.TrimSpace(runRemote(t, conn, "ssh host-b hostname")); got != "host-b" {
		t.Fatalf("host-a reached %q, want host-b", got)
	}
}

func TestIntegrationCopyBetweenHostsThroughThisOne(t *testing.T) {
	connA, connB := dialHost(t, "GOSCP_HOST_A"), dialHost(t, "GOSCP_HOST_B")
	src, dst := NewClient(connA), NewClient(connB)
	src.Recursive = true
	local := t.TempDir()
	writeTree(t, local, tree)
	onA, onB := remoteDir(t, connA), remoteDir(t, connB)
	if err := src.CopyTo(t.Context(), onA, filepath.Join(local, "project")); err != nil {
		t.Fatalf("CopyTo host-a: %v", err)
	}

	if err := CopyBetween(t.Context(), src, []string{onA + "/project"}, dst, onB); err != nil {
		t.Fatalf("CopyBetween: %v", err)
	}

	dst.Recursive = true
	back := t.TempDir()
	if err := dst.CopyFrom(t.Context(), back, onB+"/project"); err != nil {
		t.Fatalf("CopyFrom host-b: %v", err)
	}
	if got := readTree(t, back); !reflect.DeepEqual(got, tree) {
		t.Errorf("host-b holds %v\nwant %v", got, tree)
	}
}

func TestIntegrationCopyBetweenHostsOnTheFirst(t *testing.T) {
	connA, connB := dialHost(t, "GOSCP_HOST_A"), dialHost(t, "GOSCP_HOST_B")
	onHostA, onHostB := NewClient(connA), NewClient(connB)
	onHostA.Recursive, onHostB.Recursive = true, true
	local := t.TempDir()
	writeTree(t, local, tree)
	onA, onB := remoteDir(t, connA), remoteDir(t, connB)
	if err := onHostA.CopyTo(t.Context(), onA, filepath.Join(local, "project")); err != nil {
		t.Fatalf("CopyTo host-a: %v", err)
	}

	if err := onHostA.CopyOnHost(t.Context(), "host-b:"+onB, onA+"/project"); err != nil {
		t.Fatalf("CopyOnHost: %v", err)
	}

	back := t.TempDir()
	if err := onHostB.CopyFrom(t.Context(), back, onB+"/project"); err != nil {
		t.Fatalf("CopyFrom host-b: %v", err)
	}
	if got := readTree(t, back); !reflect.DeepEqual(got, tree) {
		t.Errorf("host-b holds %v\nwant %v", got, tree)
	}
}

func TestIntegrationCopyOnHostForwardsTheAgent(t *testing.T) {
	// keyless has no private key on host-a, so it can log in to host-b only with
	// the caller's key, through a forwarded agent.
	connA, connB := dialHostAs(t, "GOSCP_HOST_A", "keyless"), dialHost(t, "GOSCP_HOST_B")
	onA, onB := remoteDir(t, connA), remoteDir(t, connB)
	runRemote(t, connA, "printf forwarded > "+Quote(onA+"/note.txt"))
	c := NewClient(connA)

	if err := c.CopyOnHost(t.Context(), "host-b:"+onB, onA+"/note.txt"); err == nil {
		t.Fatal("CopyOnHost without an agent: want an error, as keyless has no key of its own")
	}

	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: testKey(t)}); err != nil {
		t.Fatal(err)
	}
	if err := agent.ForwardToAgent(connA, keyring); err != nil {
		t.Fatal(err)
	}
	c.ForwardAgent = true
	if err := c.CopyOnHost(t.Context(), "host-b:"+onB, onA+"/note.txt"); err != nil {
		t.Fatalf("CopyOnHost with the agent: %v", err)
	}
	if got := runRemote(t, connB, "cat "+Quote(onB+"/note.txt")); got != "forwarded" {
		t.Errorf("host-b holds %q", got)
	}
}
