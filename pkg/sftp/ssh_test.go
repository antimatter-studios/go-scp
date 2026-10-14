package sftp

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/antimatter-studios/go-scp/pkg/scp"
	pkgsftp "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sshHost is an SSH server on loopback for the tests. It serves the sftp
// subsystem as subsystem says, and answers scp commands with package scp's server
// half, both on the real filesystem. It notes which of the two each session used.
type sshHost struct {
	subsystem string // "serve", "refuse", or "accept" to agree and then hang up

	mu   sync.Mutex
	used []string
}

func (h *sshHost) note(kind string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.used = append(h.used, kind)
}

func (h *sshHost) sessions() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.used...)
}

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// dial starts the host and returns a connection to it.
func (h *sshHost) dial(t *testing.T) *ssh.Client {
	t.Helper()
	config := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
		return nil, nil
	}}
	config.AddHostKey(newSigner(t))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go h.serve(nc, config)
		}
	}()

	conn, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
		User:            "tester",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(newSigner(t))},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // a server this test started
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func (h *sshHost) serve(nc net.Conn, config *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(nc, config)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		ch, chReqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go h.session(ch, chReqs)
	}
}

func (h *sshHost) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "subsystem":
			var p struct{ Name string }
			ssh.Unmarshal(req.Payload, &p)
			if h.subsystem == "refuse" || p.Name != "sftp" {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			h.note("sftp")
			if h.subsystem == "serve" {
				if server, err := pkgsftp.NewServer(ch); err == nil {
					server.Serve()
				}
			}
			return
		case "exec":
			var p struct{ Command string }
			ssh.Unmarshal(req.Payload, &p)
			req.Reply(true, nil)
			h.note("scp")
			status := serveSCP(p.Command, ch)
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
			return
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// serveSCP answers an scp command on the real filesystem with package scp's
// server half.
func serveSCP(cmd string, ch ssh.Channel) uint32 {
	c, err := scp.ParseCommand(cmd)
	if err == nil && c.Sink {
		var sink *scp.DirSink
		if sink, err = scp.NewDirSink(c.Paths[0], scp.DirSinkOptions{Preserve: c.Preserve}); err == nil {
			err = scp.ReceiveAll(ch, ch, scp.ReceiveOptions{Recursive: c.Recursive}, sink)
			sink.Close()
		}
	} else if err == nil {
		// The whole filesystem, or on Windows the volume the temporary directory
		// is on, with each path named inside it.
		fsys := os.DirFS(filepath.VolumeName(os.TempDir()) + string(filepath.Separator))
		for i, p := range c.Paths {
			c.Paths[i] = strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(p, filepath.VolumeName(p))), "/")
		}
		var names []string
		if names, err = c.Expand(fsys); err == nil {
			err = scp.SendFS(ch, ch, fsys, names, scp.SendOptions{Recursive: c.Recursive, Preserve: c.Preserve})
		}
	}
	if err != nil {
		fmt.Fprintln(ch.Stderr(), err)
		return 1
	}
	return 0
}

func TestNewClientCopiesOverTheSFTPSubsystem(t *testing.T) {
	host := &sshHost{subsystem: "serve"}
	c := NewClient(host.dial(t))
	local, remote, back := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, local, map[string]string{"a": "A"})

	if err := c.CopyTo(t.Context(), remote, filepath.Join(local, "a")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if err := c.CopyFrom(t.Context(), back, filepath.Join(remote, "a")); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if got := readTree(t, back); !reflect.DeepEqual(got, map[string]string{"a": "A"}) {
		t.Errorf("copied back %v", got)
	}
}

func TestNewClientWithoutAnSFTPSessionIsUnavailable(t *testing.T) {
	refusing := NewClient((&sshHost{subsystem: "refuse"}).dial(t))
	silent := NewClient((&sshHost{subsystem: "accept"}).dial(t))
	closedConn := (&sshHost{subsystem: "serve"}).dial(t)
	closedConn.Close()

	for name, c := range map[string]*Client{"refused": refusing, "never answers": silent, "connection closed": NewClient(closedConn)} {
		t.Run(name, func(t *testing.T) {
			if err := c.CopyFrom(t.Context(), t.TempDir(), "/x"); !errors.Is(err, ErrUnavailable) {
				t.Errorf("CopyFrom = %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestAutoPicksTheProtocolTheHostOffers(t *testing.T) {
	for subsystem, want := range map[string]string{"serve": "sftp", "refuse": "scp"} {
		t.Run(want, func(t *testing.T) {
			host := &sshHost{subsystem: subsystem}
			a := NewAuto(host.dial(t))
			local, remote, back := t.TempDir(), t.TempDir(), t.TempDir()
			writeTree(t, local, map[string]string{"a": "A"})

			if err := a.CopyTo(t.Context(), remote, filepath.Join(local, "a")); err != nil {
				t.Fatalf("CopyTo: %v", err)
			}
			if err := a.CopyFrom(t.Context(), back, filepath.Join(remote, "a")); err != nil {
				t.Fatalf("CopyFrom: %v", err)
			}
			if got := readTree(t, back); !reflect.DeepEqual(got, map[string]string{"a": "A"}) {
				t.Errorf("copied back %v", got)
			}
			for _, used := range host.sessions() {
				if used != want {
					t.Errorf("sessions used %v, want only %s", host.sessions(), want)
					break
				}
			}
		})
	}
}

// Options set on Auto reach whichever client a copy uses, without being set on
// either.
func TestAutoTakesOptionsOnce(t *testing.T) {
	for subsystem, protocol := range map[string]string{"serve": "sftp", "refuse": "scp"} {
		t.Run(protocol, func(t *testing.T) {
			a := NewAuto((&sshHost{subsystem: subsystem}).dial(t))
			a.Recursive = true
			log := newProgressLog()
			a.Progress = log.record
			local, remote, back := t.TempDir(), t.TempDir(), t.TempDir()
			writeTree(t, local, tree)

			if err := a.CopyTo(t.Context(), remote, filepath.Join(local, "project")); err != nil {
				t.Fatalf("CopyTo: %v", err)
			}
			if err := a.CopyFrom(t.Context(), back, filepath.Join(remote, "project")); err != nil {
				t.Fatalf("CopyFrom: %v", err)
			}
			if got := readTree(t, back); !reflect.DeepEqual(got, tree) {
				t.Errorf("copied back %v\nwant %v", got, tree)
			}
			if want := sizes(tree); !reflect.DeepEqual(log.last, want) {
				t.Errorf("progress finished at %v\nwant %v", log.last, want)
			}
			if a.SFTP.Recursive || a.SCP.Recursive || a.SFTP.Progress != nil || a.SCP.Progress != nil {
				t.Error("Auto changed the options of its clients")
			}
		})
	}
}

// A failure over SFTP is the copy's own, not a reason to try the other protocol.
func TestAutoReturnsAnSFTPFailureAsItIs(t *testing.T) {
	host := &sshHost{subsystem: "serve"}
	a := NewAuto(host.dial(t))

	err := a.CopyFrom(t.Context(), t.TempDir(), filepath.Join(t.TempDir(), "missing"))
	if err == nil || errors.Is(err, ErrUnavailable) {
		t.Errorf("CopyFrom = %v, want the missing file reported", err)
	}
	err = a.CopyTo(t.Context(), filepath.Join(t.TempDir(), "x"), filepath.Join(t.TempDir(), "missing"))
	if err == nil || errors.Is(err, ErrUnavailable) {
		t.Errorf("CopyTo = %v, want the missing file reported", err)
	}
	for _, used := range host.sessions() {
		if used != "sftp" {
			t.Errorf("sessions used %v, want no fall back", host.sessions())
		}
	}
}
