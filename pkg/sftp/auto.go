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
// Set the shared Options on Auto itself: every copy uses them, over whichever
// protocol it takes, in place of the clients' own. Settings that only one protocol
// has, such as SFTP's Resume or SCP's Program, stay on that client.
type Auto struct {
	scp.Options

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
	overSFTP, overSCP := a.clients()
	err := overSFTP.CopyTo(ctx, target, sources...)
	if errors.Is(err, ErrUnavailable) {
		return overSCP.CopyTo(ctx, target, sources...)
	}
	return err
}

// CopyFrom copies files from the remote host into target on this host; see
// Client.CopyFrom and scp.Client.CopyFrom.
func (a *Auto) CopyFrom(ctx context.Context, target string, sources ...string) error {
	overSFTP, overSCP := a.clients()
	err := overSFTP.CopyFrom(ctx, target, sources...)
	if errors.Is(err, ErrUnavailable) {
		return overSCP.CopyFrom(ctx, target, sources...)
	}
	return err
}

// clients returns copies of the two clients carrying Auto's Options. Copies, so
// that copies running at once never write to the same client.
func (a *Auto) clients() (*Client, *scp.Client) {
	overSFTP, overSCP := *a.SFTP, *a.SCP
	overSFTP.Options, overSCP.Options = a.Options, a.Options
	return &overSFTP, &overSCP
}
