// Command sftp copies a local directory to a remote host and back again over SFTP,
// as "scp -s -r" does. With -auto it uses whichever protocol the host offers, SFTP
// or the classic scp protocol, and with -program it runs a local SFTP server
// program instead of connecting anywhere, as "scp -D" does. With -dst-addr it then
// copies the directory on from the first host to a second one through this
// program, as "scp -3" does, and back from there.
//
// It is a module of its own, as pkg/sftp is, so the rest of the examples do not
// depend on SFTP code.
//
//	go run . -addr host:22 -key ~/.ssh/id_ed25519 -dir ./project -remote /tmp -back ./restored
//	go run . -addr host:22 -key ~/.ssh/id_ed25519 -auto -dir ./project -remote /tmp -back ./restored
//	go run . -program /usr/lib/openssh/sftp-server -dir ./project -remote /tmp -back ./restored
//	go run . -addr a:22 -key k -dst-addr b:22 -dst-key k -dir ./project -remote /tmp -back ./restored
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
	dstHost := connect.Flags("dst-")
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

	if dstHost.Addr != "" {
		c = between(ctx, c, dstHost, copied, *remote)
	}

	if err := c.CopyFrom(ctx, *back, copied); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("copied %s back into %s\n", copied, *back)
}

// between copies dir on from the host c reaches to the same place on dstHost,
// through this program, and returns a client for dstHost to copy it back with.
func between(ctx context.Context, c copier, dstHost *connect.Host, dir, remote string) copier {
	src, ok := c.(*sftp.Client)
	if !ok {
		log.Fatal("-dst-addr needs a plain SFTP client: leave out -auto and -program")
	}
	conn, err := dstHost.Dial()
	if err != nil {
		log.Fatal(err)
	}
	dst := sftp.NewClient(conn)
	dst.Recursive = true
	if err := sftp.CopyBetween(ctx, src, []string{dir}, dst, remote); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("copied %s from %s to %s on %s\n", dir, "the first host", remote, dstHost.Addr)
	return dst
}
