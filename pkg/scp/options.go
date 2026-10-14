package scp

// Options are the settings that copies over either protocol share. Client embeds
// them, and so does the Client of package sftp, so a program that picks the
// protocol at run time sets them once; package sftp's Auto takes them that way.
type Options struct {
	// Recursive lets CopyTo and CopyFrom copy directories with their contents, as
	// "scp -r" does.
	Recursive bool

	// Preserve keeps file times and modes, as "scp -p" does.
	Preserve bool

	// Glob lets the remote sources given to CopyFrom hold wildcards (*, ? and
	// [...]), matched on the remote host as scp's remote paths are.
	Glob bool

	// Limit, when positive, caps each copy at that many bytes per second, as
	// "scp -l" does. scp's -l counts Kbit/s, so -l n is a Limit of n*1000/8.
	Limit int64

	// MaxSize, when positive, refuses a remote file larger than it before any of
	// it is copied. The file is that copy's failure, and the copy carries on.
	MaxSize int64

	// Progress, when set, is told how each file of every copy moves: as it starts
	// and as its bytes go, with its path in the copy and its size.
	Progress Progress
}
