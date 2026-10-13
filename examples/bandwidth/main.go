// Command bandwidth uploads a file at a limited rate, as "scp -l" does, and
// reports how long it took.
//
//	go run ./examples/bandwidth -addr host:22 -key ~/.ssh/id_ed25519 -from big.bin -to /tmp/big.bin -kbit 800
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/antimatter-studios/go-scp/examples/internal/connect"
	"github.com/antimatter-studios/go-scp/pkg/scp"
)

func main() {
	host := connect.Flags("")
	from := flag.String("from", "", "local file to send")
	to := flag.String("to", "", "path on the remote host")
	kbit := flag.Int64("kbit", 800, "limit in Kbit/s, as scp -l takes it")
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

	c := scp.NewClient(conn)
	c.Limit = *kbit * 1000 / 8 // Limit counts bytes per second
	start := time.Now()
	if err := c.WriteFile(context.Background(), *to, data, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("uploaded %d bytes at %d Kbit/s in %s\n", len(data), *kbit, time.Since(start).Round(time.Millisecond))
}
