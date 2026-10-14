package scp_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing/fstest"

	"github.com/antimatter-studios/go-scp/pkg/scp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// An SSH server answers a session that asked to run an scp command, serving the
// files under one directory and nothing outside it.
func Example_server() {
	root, err := os.OpenRoot(os.TempDir())
	if err != nil {
		return
	}
	defer root.Close()

	answer := func(command string, channel io.ReadWriter, stderr io.Writer) int {
		return scp.Serve(command, channel, stderr, root) // the session's exit status
	}
	_ = answer
}

// Upload a directory with its contents, keeping times and modes, as
// "scp -r -p ./site deploy@example.com:/srv/www" does.
func ExampleClient_CopyTo() {
	home, _ := os.UserHomeDir()
	hostKeys, err := knownhosts.New(filepath.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		log.Fatal(err)
	}
	pem, err := os.ReadFile(filepath.Join(home, ".ssh", "id_ed25519"))
	if err != nil {
		log.Fatal(err)
	}
	key, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := ssh.Dial("tcp", "example.com:22", &ssh.ClientConfig{
		User:            "deploy",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: hostKeys,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	c := scp.NewClient(conn)
	c.Recursive, c.Preserve = true, true
	if err := c.CopyTo(context.Background(), "/srv/www", "./site"); err != nil {
		log.Fatal(err)
	}
}

// Download the logs matching a wildcard into a local directory, as
// "scp 'backup@example.com:/var/log/app/*.log' ./logs" does, refusing any file
// larger than 100 MiB and writing each one whole or not at all.
func ExampleClient_CopyFrom() {
	var conn *ssh.Client // dialled as in the CopyTo example

	c := scp.NewClient(conn)
	c.Glob = true
	c.MaxSize = 100 << 20
	c.Atomic = true
	c.Progress = func(name string, done, size int64) {
		fmt.Printf("\r%s: %d of %d bytes", name, done, size)
	}
	if err := c.CopyFrom(context.Background(), "./logs", "/var/log/app/*.log"); err != nil {
		log.Fatal(err)
	}
}

// Serve a drop box: clients may send files of up to 100 MiB, never fetch any, and
// a program watching the directory never sees half a file.
func ExampleServer() {
	root, err := os.OpenRoot("/srv/inbox")
	if err != nil {
		log.Fatal(err)
	}
	defer root.Close()
	inbox := &scp.Server{Root: root, WriteOnly: true, MaxSize: 100 << 20, Atomic: true}

	// For each SSH session that asks to run a command:
	answer := func(command string, channel io.ReadWriter, stderr io.Writer) int {
		return inbox.Serve(command, channel, stderr) // the session's exit status
	}
	_ = answer
}

// A copy that skips a file goes on with the rest, and Failures says which it
// skipped. Here the two ends of a transfer are this package's own, joined by pipes:
// SendFS sends three names, one of which does not exist, and ReceiveAll writes what
// arrives to a directory.
func ExampleFailures() {
	files := fstest.MapFS{"a.txt": {Data: []byte("A")}, "c.txt": {Data: []byte("C")}}
	dir, err := os.MkdirTemp("", "received")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sink, err := scp.NewDirSink(dir, scp.DirSinkOptions{})
	if err != nil {
		log.Fatal(err)
	}
	defer sink.Close()

	toReceiver, fromSender := io.Pipe()
	toSender, fromReceiver := io.Pipe()
	received := make(chan error)
	go func() {
		received <- scp.ReceiveAll(toReceiver, fromReceiver, scp.ReceiveOptions{}, sink)
		fromReceiver.Close()
	}()
	err = scp.SendFS(toSender, fromSender, files, []string{"a.txt", "b.txt", "c.txt"}, scp.SendOptions{})
	fromSender.Close()
	<-received

	skipped, rest := scp.Failures(err)
	for _, f := range skipped {
		fmt.Printf("skipped %s: %v\n", f.Path, f.Err)
	}
	if rest != nil {
		log.Fatal(rest) // the copy stopped part-way
	}
	// Output:
	// skipped b.txt: scp: b.txt: open b.txt: file does not exist
}
