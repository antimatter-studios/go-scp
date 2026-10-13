// Command upload copies one local file to a remote host, as "scp file host:path"
// does.
//
//	go run ./examples/upload -addr host:22 -key ~/.ssh/id_ed25519 -from notes.txt -to /tmp/notes.txt
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
	from := flag.String("from", "", "local file to send")
	to := flag.String("to", "", "path on the remote host")
	flag.Parse()

	conn, err := host.Dial()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	data, err := os.ReadFile(*from)
	if err != nil {
		log.Fatal(err)
	}
	info, err := os.Stat(*from)
	if err != nil {
		log.Fatal(err)
	}

	c := scp.NewClient(conn)
	if err := c.WriteFile(context.Background(), *to, data, info.Mode().Perm()); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("uploaded %s (%d bytes) to %s\n", *from, len(data), *to)
}
