package sftp

import (
	"context"
	"errors"

	"github.com/antimatter-studios/go-scp/pkg/scp"
	"golang.org/x/crypto/ssh"
)

// Auto copies over SFTP when the host offers it, and over scp's classic protocol
// when it does not: a host with no SFTP subsystem still has its scp program, and
// one that offers only SFTP has no scp program to run.
//
// Set options on both clients; each copy uses only one of them.
type Auto struct {
	SFTP *Client
	SCP  *scp.Client
}

// NewAuto returns an Auto that copies over conn.
func NewAuto(conn *ssh.Client) *Auto {
	return &Auto{SFTP: NewClient(conn), SCP: scp.NewClient(conn)}
}

// CopyTo copies local files to target on the remote host; see Client.CopyTo and
// scp.Client.CopyTo.
func (a *Auto) CopyTo(ctx context.Context, target string, sources ...string) error {
	err := a.SFTP.CopyTo(ctx, target, sources...)
	if errors.Is(err, ErrUnavailable) {
		return a.SCP.CopyTo(ctx, target, sources...)
	}
	return err
}

// CopyFrom copies files from the remote host into target on this host; see
// Client.CopyFrom and scp.Client.CopyFrom.
func (a *Auto) CopyFrom(ctx context.Context, target string, sources ...string) error {
	err := a.SFTP.CopyFrom(ctx, target, sources...)
	if errors.Is(err, ErrUnavailable) {
		return a.SCP.CopyFrom(ctx, target, sources...)
	}
	return err
}
