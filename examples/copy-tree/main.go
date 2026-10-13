// Command copy-tree copies a local directory to a remote host and back again, as
// "scp -r dir host:path" and "scp -r host:path dir" do.
//
//	go run ./examples/copy-tree -addr host:22 -key ~/.ssh/id_ed25519 -dir ./project -remote /tmp -back ./restored
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"path/filepath"

	"github.com/antimatter-studios/go-scp/examples/internal/connect"
	"github.com/antimatter-studios/go-scp/pkg/scp"
)

func main() {
	host := connect.Flags("")
	dir := flag.String("dir", "", "local directory to send")
	remote := flag.String("remote", "", "existing directory on the remote host to copy it into")
	back := flag.String("back", "", "local directory to copy it back into")
	flag.Parse()

	conn, err := host.Dial()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	c := scp.NewClient(conn)
	c.Recursive = true // scp -r
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
