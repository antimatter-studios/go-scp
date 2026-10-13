// Command on-host asks a remote host to copy its own files to another host, as
// "scp -R" does: the data goes between the two hosts directly. The first host
// needs a way into the second: a key of its own or, with -forward-agent, the keys
// in your ssh-agent, as "scp -A -R" does. The second host must be in its
// known_hosts either way.
//
//	go run ./examples/on-host -addr a:22 -key k -from /srv/data -to b:/srv
//	go run ./examples/on-host -addr a:22 -key k -forward-agent -from /srv/data -to b:/srv
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"

	"golang.org/x/crypto/ssh/agent"

	"github.com/antimatter-studios/go-scp/examples/internal/connect"
	"github.com/antimatter-studios/go-scp/pkg/scp"
)

func main() {
	host := connect.Flags("")
	from := flag.String("from", "", "path on the connected host")
	to := flag.String("to", "", "where it goes, as other-host:path")
	forward := flag.Bool("forward-agent", false, "let the connected host reach the other one with the keys in your ssh-agent")
	flag.Parse()

	conn, err := host.Dial()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	c := scp.NewClient(conn)
	c.Recursive = true
	if *forward {
		socket := os.Getenv("SSH_AUTH_SOCK")
		if socket == "" {
			log.Fatal("-forward-agent needs a running ssh-agent: SSH_AUTH_SOCK is not set")
		}
		sock, err := net.Dial("unix", socket)
		if err != nil {
			log.Fatal(err)
		}
		defer sock.Close()
		// Once per connection: every session the client opens then asks for it.
		if err := agent.ForwardToAgent(conn, agent.NewClient(sock)); err != nil {
			log.Fatal(err)
		}
		c.ForwardAgent = true
	}
	if err := c.CopyOnHost(context.Background(), *to, *from); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s copied %s to %s\n", host.Addr, *from, *to)
}
