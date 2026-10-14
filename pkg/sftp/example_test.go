package sftp_test

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"github.com/antimatter-studios/go-scp/pkg/scp"
	"github.com/antimatter-studios/go-scp/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Upload a directory over SFTP, eight files at a time, as
// "scp -s -r ./photos deploy@example.com:/srv/photos" does, and list any file
// it skipped.
func ExampleClient_CopyTo() {
	home, _ := os.UserHomeDir()
	hostKeys, err := knownhosts.New(filepath.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		log.Fatal(err)
	}
	pem, err := os.ReadFile(filepath.Join(home, ".ssh", "id_ed25519"))
	if err != nil {
		log.Fatal(err)
	}
	key, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := ssh.Dial("tcp", "example.com:22", &ssh.ClientConfig{
		User:            "deploy",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: hostKeys,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	c := sftp.NewClient(conn)
	c.Recursive = true
	c.Parallel = 8
	err = c.CopyTo(context.Background(), "/srv/photos", "./photos")
	skipped, rest := scp.Failures(err)
	for _, f := range skipped {
		log.Printf("skipped %s: %v", f.Path, f.Err)
	}
	if rest != nil {
		log.Fatal(rest)
	}
}

// Copy over whichever protocol the host offers: SFTP when it has the subsystem,
// and the classic scp protocol when it does not. The options are set once, on
// Auto, and apply over either.
func ExampleAuto() {
	var conn *ssh.Client // dialled as in the Client.CopyTo example

	a := sftp.NewAuto(conn)
	a.Recursive = true
	a.Preserve = true
	a.Limit = 1 << 20 // bytes per second
	if err := a.CopyFrom(context.Background(), "./backup", "/etc/app"); err != nil {
		log.Fatal(err)
	}
}
