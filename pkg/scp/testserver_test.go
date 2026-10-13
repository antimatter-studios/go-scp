package scp

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// handler answers one exec request: it gets the command, the session's stdin,
// stdout and stderr, and returns the exit status sent back.
type handler func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int

// testServer is an SSH server on the loopback interface, so transfers are tested
// over a real handshake and real sessions.
type testServer struct {
	addr    string
	port    string
	keyPath string // the one client key it accepts, for clients to dial with
}

func newTestServer(t *testing.T, h handler) *testServer {
	t.Helper()

	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}

	clientPub, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(clientKey, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	authorized, err := ssh.NewPublicKey(clientPub)
	if err != nil {
		t.Fatal(err)
	}

	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(authorized.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("key not authorized")
		},
	}
	config.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConn(conn, config, h)
		}
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	return &testServer{addr: host, port: port, keyPath: keyPath}
}

func serveConn(conn net.Conn, config *ssh.ServerConfig, h handler) {
	sc, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		conn.Close()
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "sessions only")
			continue
		}
		ch, requests, err := nc.Accept()
		if err != nil {
			continue
		}
		go serveSession(ch, requests, h)
	}
}

func serveSession(ch ssh.Channel, requests <-chan *ssh.Request, h handler) {
	defer ch.Close()
	for req := range requests {
		if req.Type != "exec" {
			req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			req.Reply(false, nil)
			return
		}
		req.Reply(true, nil)
		status := make([]byte, 4)
		binary.BigEndian.PutUint32(status, uint32(h(payload.Command, ch, ch, ch.Stderr())))
		ch.SendRequest("exit-status", false, status)
		return
	}
}

// dial connects a Client to s with its key.
func (s *testServer) dial(t *testing.T) *Client {
	t.Helper()
	pem, err := os.ReadFile(s.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := ssh.Dial("tcp", net.JoinHostPort(s.addr, s.port), &ssh.ClientConfig{
		User:            "tester",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // a throwaway server in this test
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return NewClient(conn)
}

// rootFS is the whole local filesystem as an fs.FS, so absolute test paths can be
// served: "/tmp/x" is "tmp/x" in it.
var rootFS = os.DirFS("/")

func fsPath(p string) string { return strings.TrimPrefix(filepath.ToSlash(p), "/") }

// fileServer answers scp commands with this package's server half over the local
// filesystem: ReceiveAll into a DirSink for -t, SendFS for -f.
func fileServer(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
	c, err := ParseCommand(cmd)
	if err != nil {
		io.WriteString(stderr, err.Error()+"\n")
		return 127
	}

	if !c.Sink {
		for i, path := range c.Paths {
			c.Paths[i] = fsPath(path)
		}
		names, err := c.Expand(rootFS)
		if err != nil {
			io.WriteString(stderr, err.Error()+"\n")
			return 1
		}
		if err := SendFS(stdin, stdout, rootFS, names, SendOptions{Recursive: c.Recursive, Preserve: c.Preserve}); err != nil {
			io.WriteString(stderr, err.Error()+"\n")
			return 1
		}
		return 0
	}

	var sink Sink
	target := c.Paths[0]
	if info, err := os.Stat(target); c.TargetIsDir && (err != nil || !info.IsDir()) {
		sink = failingSink{fmt.Errorf("scp: %s: Not a directory", target)}
	} else if dir, err := NewDirSink(target, DirSinkOptions{Preserve: c.Preserve}); err != nil {
		sink = failingSink{fmt.Errorf("scp: %s: %s", target, describe(err))}
	} else {
		defer dir.Close()
		sink = dir
	}
	if err := ReceiveAll(stdin, stdout, ReceiveOptions{Recursive: c.Recursive}, sink); err != nil {
		// As scp does, including the failures the sending side reported.
		io.WriteString(stderr, err.Error()+"\n")
		return 1
	}
	return 0
}

// failingSink refuses everything with one error, for a target that cannot take
// anything; the sender is still told, entry by entry, as scp would tell it.
type failingSink struct{ err error }

func (f failingSink) File(Header, io.Reader) error { return f.err }
func (f failingSink) EnterDir(Header) error        { return f.err }
func (f failingSink) LeaveDir() error              { return f.err }
