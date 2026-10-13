// Command sudo writes a file where the login user may not, by running scp through
// sudo on the remote host. The user needs a sudo rule that lets them run scp
// without a password.
//
//	go run ./examples/sudo -addr host:22 -key ~/.ssh/id_ed25519 -to /srv/root-only/settings.txt -text 'hello'
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
	to := flag.String("to", "", "path on the remote host")
	text := flag.String("text", "", "what to write")
	flag.Parse()

	conn, err := host.Dial()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	c := scp.NewClient(conn)
	c.Program = "sudo -n scp" // -n: fail rather than wait for a password prompt
	if err := c.WriteFile(context.Background(), *to, []byte(*text+"\n"), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s as root\n", *to)
}
