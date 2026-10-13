// Package scp implements both ends of the classic scp file-copy protocol: the one
// OpenSSH's scp program speaks over an SSH channel when it is not using SFTP.
//
// The protocol is a conversation between two copies of scp. The end that hands
// files over runs as "scp -f <path>" (from); the end that takes them in runs as
// "scp -t <path>" (to). They exchange a control line per file or directory, the
// files' bytes, and a status byte after each step: 0 for success, or 1 or 2
// followed by a message ending in a newline. With -p a time line goes ahead of
// each entry.
//
// [Client] copies files and directories to and from a remote host over an
// established golang.org/x/crypto/ssh connection, with the remote host's own scp
// program as the other end, and between two remote hosts with [CopyBetween] and
// [Client.CopyOnHost]. Nothing runs locally but Go.
//
// [Receive], [ReceiveAll], [Send] and [SendFS] are the other end, for an SSH
// server that handles transfers itself instead of running scp: give them the
// stdin and stdout of a session that was asked to run "scp -t" or "scp -f", as
// [ParseCommand] reads it.
//
// The README's feature table lists what is supported, against the options of
// OpenSSH's scp.
package scp
