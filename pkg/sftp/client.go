// Package sftp copies files to and from a remote host over SFTP, as OpenSSH's
// "scp -s" does, in the same shape as package scp's Client: CopyTo and CopyFrom,
// with Recursive, Preserve and Glob.
//
// SFTP reaches hosts that the classic scp protocol cannot: a host that offers only
// the SFTP subsystem, with no shell and no scp program. Auto picks whichever of the
// two protocols a host offers.
//
// This package lives in a module of its own, so a program that uses only package
// scp does not depend on github.com/pkg/sftp, which this package is built on.
package sftp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"

	pkgsftp "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Client copies files over SFTP. Each transfer opens its own SFTP session, so one
// Client serves any number of them.
type Client struct {
	// session starts an SFTP session for one transfer, and returns it with the
	// function that ends it. A field so tests can give the client a server of
	// their own.
	session func(opts ...pkgsftp.ClientOption) (*pkgsftp.Client, func() error, error)

	// Recursive lets CopyTo and CopyFrom copy directories with their contents, as
	// "scp -r" does.
	Recursive bool

	// Preserve keeps modification times and modes, as "scp -p" does.
	Preserve bool

	// Glob lets the remote sources given to CopyFrom hold wildcards (*, ? and
	// [...]), matched on the remote host as scp's remote paths are.
	Glob bool

	// Requests caps how many requests may be in flight for one file, as
	// "-X nrequests=n" does; 0 leaves github.com/pkg/sftp's default.
	Requests int

	// BufferSize is the most one request carries, as "-X buffer=n" does; 0 leaves
	// the default of 32768 bytes, the size every server must accept. A larger size
	// works only with a server that allows it.
	BufferSize int

	// Limit, when positive, caps each copy at that many bytes per second, as
	// "scp -l" does. scp's -l counts Kbit/s, so -l n is a Limit of n*1000/8.
	Limit int64

	// MaxSize, when positive, refuses a remote file larger than it: CopyFrom
	// reports it as that source's failure, writes none of it, and carries on.
	MaxSize int64

	// Resume continues a file that a copy left part-way, as sftp's reget and
	// reput do: where the destination already holds the start of a file, CopyTo
	// and CopyFrom copy only the rest. A destination longer than its source is
	// that file's failure. Nothing checks that the part already there matches.
	Resume bool
}

// ErrUnavailable is returned, wrapped, when an SFTP session cannot be started: the
// host offers no SFTP subsystem, or the program given to NewProgramClient did not
// start or did not speak SFTP. Auto falls back to the classic protocol on it.
var ErrUnavailable = errors.New("sftp: no SFTP session")

// NewClient returns a Client that copies over conn, through the host's SFTP
// subsystem. Verifying the host's key is conn's business, settled when it was
// dialled.
func NewClient(conn *ssh.Client) *Client {
	return &Client{session: func(opts ...pkgsftp.ClientOption) (*pkgsftp.Client, func() error, error) {
		// The session is opened here rather than by github.com/pkg/sftp's
		// NewClient, which leaves it open when the host refuses the subsystem;
		// Auto would leak one on every fall back.
		s, err := conn.NewSession()
		if err != nil {
			return nil, nil, err
		}
		// The pipes fail only once the session has started, which it has not.
		stdin, inErr := s.StdinPipe()
		stdout, outErr := s.StdoutPipe()
		err = errors.Join(inErr, outErr)
		if err == nil {
			err = s.RequestSubsystem("sftp")
		}
		var c *pkgsftp.Client
		if err == nil {
			c, err = pkgsftp.NewClientPipe(stdout, stdin, opts...)
		}
		if err != nil {
			s.Close()
			return nil, nil, err
		}
		return c, func() error {
			c.Close()
			return s.Close()
		}, nil
	}}
}

// NewProgramClient returns a Client that, for each transfer, runs an SFTP server
// program on this host and speaks SFTP over its standard input and output, as
// "scp -D program" does. There is no SSH connection, so the "remote" files are
// local ones, seen through the program.
func NewProgramClient(program string, args ...string) *Client {
	return &Client{session: func(opts ...pkgsftp.ClientOption) (*pkgsftp.Client, func() error, error) {
		cmd := exec.Command(program, args...)
		// The pipes fail only once the program has started, which it has not.
		stdin, inErr := cmd.StdinPipe()
		stdout, outErr := cmd.StdoutPipe()
		err := errors.Join(inErr, outErr)
		if err == nil {
			err = cmd.Start()
		}
		if err != nil {
			return nil, nil, err
		}
		c, err := pkgsftp.NewClientPipe(stdout, stdin, opts...)
		if err != nil {
			stdin.Close()
			cmd.Wait()
			return nil, nil, err
		}
		return c, func() error {
			c.Close()
			return cmd.Wait()
		}, nil
	}}
}

// with starts an SFTP session, runs f on it, and ends it. Cancelling ctx ends the
// session, which ends f; the error is then ctx's.
func (c *Client) with(ctx context.Context, f func(*pkgsftp.Client) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var opts []pkgsftp.ClientOption
	if c.Requests > 0 {
		opts = append(opts, pkgsftp.MaxConcurrentRequestsPerFile(c.Requests))
	}
	if c.BufferSize > 0 {
		opts = append(opts, pkgsftp.MaxPacketUnchecked(c.BufferSize))
	}
	client, end, err := c.session(opts...)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var once sync.Once
	endSession := func() { once.Do(func() { end() }) }
	stop := context.AfterFunc(ctx, endSession)
	err = f(client)
	stop()
	endSession()
	if ctxErr := ctx.Err(); err != nil && ctxErr != nil {
		return ctxErr
	}
	return err
}
