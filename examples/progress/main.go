// Command progress copies a local directory to a remote host, as "scp -r" does,
// and prints each file as it starts and as it finishes.
//
//	go run ./examples/progress -addr host:22 -key ~/.ssh/id_ed25519 -dir ./project -remote /tmp
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/antimatter-studios/go-scp/examples/internal/connect"
	"github.com/antimatter-studios/go-scp/pkg/scp"
)

func main() {
	host := connect.Flags("")
	dir := flag.String("dir", "", "local directory to send")
	remote := flag.String("remote", "", "existing directory on the remote host to copy it into")
	flag.Parse()

	conn, err := host.Dial()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	c := scp.NewClient(conn)
	c.Recursive = true
	// Called as each file starts, with done 0, and as its bytes move.
	c.Progress = func(name string, done, size int64) {
		switch done {
		case 0:
			fmt.Printf("%s: starting, %d bytes\n", name, size)
		case size:
			fmt.Printf("%s: done\n", name)
		}
	}
	if err := c.CopyTo(context.Background(), *remote, *dir); err != nil {
		log.Fatal(err)
	}
}
