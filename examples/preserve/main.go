// Command preserve uploads a file keeping its modification time and mode, as
// "scp -p" does, then reads the times back from the remote host.
//
//	go run ./examples/preserve -addr host:22 -key ~/.ssh/id_ed25519 -from notes.txt -to /tmp/notes.txt
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
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

	file, err := os.Open(*from)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		log.Fatal(err)
	}

	c := scp.NewClient(conn)
	c.Preserve = true // scp -p
	ctx := context.Background()
	h := scp.Header{Mode: info.Mode().Perm(), Size: info.Size(), ModTime: info.ModTime(), AccessTime: info.ModTime()}
	if err := c.Upload(ctx, *to, h, file); err != nil {
		log.Fatal(err)
	}

	got, err := c.Download(ctx, *to, io.Discard)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("remote %s: mode %04o, modified %s\n", *to, got.Mode.Perm(), got.ModTime.UTC().Format("2006-01-02T15:04:05Z"))
}
