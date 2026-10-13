// Command download copies one file from a remote host into a local file, as
// "scp host:path file" does, and prints its header.
//
//	go run ./examples/download -addr host:22 -key ~/.ssh/id_ed25519 -from /etc/hostname -to hostname.txt
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/antimatter-studios/go-scp/examples/internal/connect"
	"github.com/antimatter-studios/go-scp/pkg/scp"
)

func main() {
	host := connect.Flags("")
	from := flag.String("from", "", "path on the remote host")
	to := flag.String("to", "", "local file to write")
	flag.Parse()

	conn, err := host.Dial()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	out, err := os.Create(*to)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()

	// Download streams into any io.Writer; ReadFile returns the bytes instead.
	h, err := scp.NewClient(conn).Download(context.Background(), *from, out)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("downloaded %s: %d bytes, mode %04o\n", h.Name, h.Size, h.Mode.Perm())
}
