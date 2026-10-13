// Command sftp copies a local directory to a remote host and back again over SFTP,
// as "scp -s -r" does. With -auto it uses whichever protocol the host offers, SFTP
// or the classic scp protocol, and with -program it runs a local SFTP server
// program instead of connecting anywhere, as "scp -D" does.
//
// It is a module of its own, as pkg/sftp is, so the rest of the examples do not
// depend on SFTP code.
//
//	go run . -addr host:22 -key ~/.ssh/id_ed25519 -dir ./project -remote /tmp -back ./restored
//	go run . -addr host:22 -key ~/.ssh/id_ed25519 -auto -dir ./project -remote /tmp -back ./restored
//	go run . -program /usr/lib/openssh/sftp-server -dir ./project -remote /tmp -back ./restored
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"path/filepath"

	"github.com/antimatter-studios/go-scp/examples/internal/connect"
	"github.com/antimatter-studios/go-scp/pkg/sftp"
)

// copier is what both sftp.Client and sftp.Auto offer.
type copier interface {
	CopyTo(ctx context.Context, target string, sources ...string) error
	CopyFrom(ctx context.Context, target string, sources ...string) error
}

func main() {
	host := connect.Flags("")
	dir := flag.String("dir", "", "local directory to send")
	remote := flag.String("remote", "", "existing directory on the remote host to copy it into")
	back := flag.String("back", "", "local directory to copy it back into")
	auto := flag.Bool("auto", false, "use SFTP when the host offers it, and the classic scp protocol when not")
	program := flag.String("program", "", "run this SFTP server program locally instead of connecting, as scp -D does")
	flag.Parse()

	var c copier
	switch {
	case *program != "":
		client := sftp.NewProgramClient(*program)
		client.Recursive = true
		c = client
	default:
		conn, err := host.Dial()
		if err != nil {
			log.Fatal(err)
		}
		defer conn.Close()
		if *auto {
			a := sftp.NewAuto(conn)
			a.SFTP.Recursive, a.SCP.Recursive = true, true
			c = a
		} else {
			client := sftp.NewClient(conn)
			client.Recursive = true
			c = client
		}
	}
	ctx := context.Background()

	if err := c.CopyTo(ctx, *remote, *dir); err != nil {
		log.Fatal(err)
	}
	copied := *remote + "/" + filepath.Base(*dir)
	fmt.Printf("copied %s to %s\n", *dir, copied)

	if err := c.CopyFrom(ctx, *back, copied); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("copied %s back into %s\n", copied, *back)
}
