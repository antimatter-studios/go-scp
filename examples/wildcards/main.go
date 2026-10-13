// Command wildcards fetches every remote file matching a pattern, as
// "scp 'host:logs/*.txt' dir" does: the remote host's shell expands the pattern.
//
//	go run ./examples/wildcards -addr host:22 -key ~/.ssh/id_ed25519 -pattern '/var/log/app/*.log' -to ./logs
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
	pattern := flag.String("pattern", "", "remote path with wildcards")
	to := flag.String("to", "", "existing local directory to copy the matches into")
	flag.Parse()

	conn, err := host.Dial()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	c := scp.NewClient(conn)
	// Glob leaves *, ? and [...] for the remote shell; the rest stays quoted. Only
	// names matching the pattern are accepted back.
	c.Glob = true
	if err := c.CopyFrom(context.Background(), *to, *pattern); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("copied the matches of %s into %s\n", *pattern, *to)
}
