package scp

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
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

// scpCommand reads the direction flag, whether -p was given, and the path from an
// scp command line. The path may be quoted as Quote does, or bare as OpenSSH's own
// client sends it.
func scpCommand(cmd string) (flag string, preserve bool, path string, ok bool) {
	fields := strings.Fields(cmd)
	if len(fields) < 3 || fields[0] != "scp" {
		return "", false, "", false
	}
	for _, f := range fields[1 : len(fields)-1] {
		switch f {
		case "-t", "-f":
			flag = f
		case "-p":
			preserve = true
		}
	}
	rest := strings.TrimSpace(cmd[strings.Index(cmd, flag)+len(flag):])
	if rest == "--" || strings.HasPrefix(rest, "-- ") {
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "--"))
	}
	if len(rest) >= 2 && rest[0] == '\'' && rest[len(rest)-1] == '\'' {
		rest = strings.ReplaceAll(rest[1:len(rest)-1], `'\''`, `'`)
	}
	return flag, preserve, rest, flag != "" && rest != ""
}

// fileServer answers scp commands with Receive and Send over the local filesystem,
// writing a received file the way scp does: in place, then cut to length. With -p
// it sends a file's modification time, and applies received times and the mode.
func fileServer(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
	flag, preserve, name, ok := scpCommand(cmd)
	if !ok {
		io.WriteString(stderr, "sh: "+strings.Fields(cmd + " x")[0]+": not found\n")
		return 127
	}

	if flag == "-f" {
		file, err := os.Open(name)
		if err != nil {
			SendFailure(stdin, stdout, "scp: "+name+": "+reason(err))
			return 1
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			SendFailure(stdin, stdout, "scp: "+name+": not a regular file")
			return 1
		}
		h := Header{Mode: info.Mode().Perm(), Size: info.Size(), Name: filepath.Base(name)}
		if preserve {
			h.ModTime, h.AccessTime = info.ModTime(), info.ModTime()
		}
		if err := Send(stdin, stdout, h, file); err != nil {
			return 1
		}
		return 0
	}

	err := Receive(stdin, stdout, 0, func(h Header, body io.Reader) error {
		file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE, h.Mode)
		if err != nil {
			return &Rejection{Message: "scp: " + name + ": " + reason(err), Err: err}
		}
		defer file.Close()
		if _, err := io.Copy(file, body); err != nil {
			return err
		}
		if err := file.Truncate(h.Size); err != nil {
			return err
		}
		if !preserve {
			return nil
		}
		if err := file.Chmod(h.Mode); err != nil {
			return err
		}
		if h.ModTime.IsZero() {
			return nil
		}
		return os.Chtimes(name, h.AccessTime, h.ModTime)
	})
	if err != nil {
		return 1
	}
	return 0
}

func reason(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "No such file or directory"
	case errors.Is(err, fs.ErrPermission):
		return "Permission denied"
	default:
		return err.Error()
	}
}
