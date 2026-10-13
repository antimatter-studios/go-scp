// Package connect holds what every example needs before it can use scp: the
// flags that name a host, and an SSH connection to it. It is the part of each
// program that is plain golang.org/x/crypto/ssh and nothing to do with scp.
package connect

import (
	"flag"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Host is how to reach one SSH host.
type Host struct {
	Addr     string // host:port
	User     string
	Key      string // private key file
	Insecure bool   // skip host key checking: throwaway test hosts only
}

// Flags registers the flags for a host, with prefix in front of each name ("" for
// -addr, -user, -key, -insecure; "dst-" for -dst-addr and so on).
func Flags(prefix string) *Host {
	h := &Host{}
	flag.StringVar(&h.Addr, prefix+"addr", "", "remote host as host:port")
	flag.StringVar(&h.User, prefix+"user", os.Getenv("USER"), "user to log in as")
	flag.StringVar(&h.Key, prefix+"key", "", "private key file")
	flag.BoolVar(&h.Insecure, prefix+"insecure", false, "skip host key checking (throwaway test hosts only)")
	return h
}

// Dial connects to the host with its key, checking the host's key against
// ~/.ssh/known_hosts unless Insecure is set.
func (h *Host) Dial() (*ssh.Client, error) {
	pem, err := os.ReadFile(h.Key)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return nil, err
	}
	hostKeys := ssh.InsecureIgnoreHostKey()
	if !h.Insecure {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		if hostKeys, err = knownhosts.New(filepath.Join(home, ".ssh", "known_hosts")); err != nil {
			return nil, err
		}
	}
	return ssh.Dial("tcp", h.Addr, &ssh.ClientConfig{
		User:            h.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeys,
	})
}
