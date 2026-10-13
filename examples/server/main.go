// Command server is an SSH server that answers scp, serving the files under one
// directory with this package's server half instead of running the scp program.
// Any scp client can use it, OpenSSH's own with -O.
//
//	go run ./examples/server -listen 127.0.0.1:2200 -root ./share -host-key host_key -authorized-key id_ed25519.pub
//	scp -O -P 2200 notes.txt me@127.0.0.1:notes.txt
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"

	"github.com/antimatter-studios/go-scp/pkg/scp"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:2200", "address to listen on")
	root := flag.String("root", ".", "directory to serve")
	hostKeyFile := flag.String("host-key", "", "this server's private host key")
	authorizedFile := flag.String("authorized-key", "", "the one public key allowed to log in")
	flag.Parse()

	config, err := serverConfig(*hostKeyFile, *authorizedFile)
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("serving %s on %s", *root, ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go serveConn(conn, config, *root)
	}
}

func serverConfig(hostKeyFile, authorizedFile string) (*ssh.ServerConfig, error) {
	hostPEM, err := os.ReadFile(hostKeyFile)
	if err != nil {
		return nil, err
	}
	hostKey, err := ssh.ParsePrivateKey(hostPEM)
	if err != nil {
		return nil, err
	}
	authorizedLine, err := os.ReadFile(authorizedFile)
	if err != nil {
		return nil, err
	}
	authorized, _, _, _, err := ssh.ParseAuthorizedKey(authorizedLine)
	if err != nil {
		return nil, err
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), authorized.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("key not authorized")
		},
	}
	config.AddHostKey(hostKey)
	return config, nil
}

func serveConn(conn net.Conn, config *ssh.ServerConfig, root string) {
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
		go serveSession(ch, requests, root)
	}
}

// serveSession answers one session's exec request, which an scp client sends as
// "scp -t <path>" or "scp -f <path>". Anything else is refused.
func serveSession(ch ssh.Channel, requests <-chan *ssh.Request, root string) {
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

		status := uint32(0)
		if err := serveScp(payload.Command, root, ch); err != nil {
			io.WriteString(ch.Stderr(), err.Error()+"\n")
			status = 1
		}
		exit := make([]byte, 4)
		binary.BigEndian.PutUint32(exit, status)
		ch.SendRequest("exit-status", false, exit)
		return
	}
}

func serveScp(command, root string, ch io.ReadWriter) error {
	cmd, err := scp.ParseCommand(command)
	if err != nil {
		return err
	}
	cmd = cmd.Relative() // every path now names something under root
	if cmd.Sink {        // scp -t: the client sends files
		sink, err := scp.NewDirSink(filepath.Join(root, filepath.FromSlash(cmd.Paths[0])), scp.DirSinkOptions{Preserve: cmd.Preserve})
		if err != nil {
			return err
		}
		defer sink.Close()
		return scp.ReceiveAll(ch, ch, scp.ReceiveOptions{Recursive: cmd.Recursive}, sink)
	}
	dir, err := os.OpenRoot(root) // scp -f: the client fetches files
	if err != nil {
		return err
	}
	defer dir.Close()
	fsys := dir.FS()
	names, err := cmd.Expand(fsys)
	if err != nil {
		return err
	}
	return scp.SendFS(ch, ch, fsys, names, scp.SendOptions{Recursive: cmd.Recursive, Preserve: cmd.Preserve})
}
