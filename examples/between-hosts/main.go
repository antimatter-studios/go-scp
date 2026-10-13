// Command between-hosts copies files from one remote host to another through this
// program, as "scp -3" does. Nothing is written to this machine.
//
//	go run ./examples/between-hosts -addr a:22 -key k -dst-addr b:22 -dst-key k -from /srv/data -to /srv
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
	srcHost := connect.Flags("")
	dstHost := connect.Flags("dst-")
	from := flag.String("from", "", "path on the first host")
	to := flag.String("to", "", "existing directory on the second host")
	flag.Parse()

	srcConn, err := srcHost.Dial()
	if err != nil {
		log.Fatal(err)
	}
	defer srcConn.Close()
	dstConn, err := dstHost.Dial()
	if err != nil {
		log.Fatal(err)
	}
	defer dstConn.Close()

	src, dst := scp.NewClient(srcConn), scp.NewClient(dstConn)
	src.Recursive = true // the source client's settings shape the copy
	if err := scp.CopyBetween(context.Background(), src, []string{*from}, dst, *to); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("copied %s on %s into %s on %s\n", *from, srcHost.Addr, *to, dstHost.Addr)
}
