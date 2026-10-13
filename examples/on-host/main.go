// Command on-host asks a remote host to copy its own files to another host, as
// "scp -R" does: the data goes between the two hosts directly. The first host
// needs its own way into the second (a key, and the second host in known_hosts).
//
//	go run ./examples/on-host -addr a:22 -key k -from /srv/data -to b:/srv
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
	from := flag.String("from", "", "path on the connected host")
	to := flag.String("to", "", "where it goes, as other-host:path")
	flag.Parse()

	conn, err := host.Dial()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	c := scp.NewClient(conn)
	c.Recursive = true
	if err := c.CopyOnHost(context.Background(), *to, *from); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s copied %s to %s\n", host.Addr, *from, *to)
}
