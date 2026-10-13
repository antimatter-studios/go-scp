// Package scp implements both ends of the classic scp file-copy protocol: the one
// OpenSSH's scp program speaks over an SSH channel when it is not using SFTP.
//
// The protocol is a conversation between two copies of scp. The end that hands a
// file over runs as "scp -f <path>" (from); the end that takes a file in runs as
// "scp -t <path>" (to). They exchange one control line per file, the file's bytes,
// and a status byte after each step: 0 for success, or 1 or 2 followed by a message
// ending in a newline.
//
// [Client] copies a single file to or from a remote host over an established
// golang.org/x/crypto/ssh connection, with the remote host's own scp program as the
// other end. Nothing runs locally but Go.
//
// [Receive] and [Send] are the other end, for an SSH server that handles transfers
// itself instead of running scp: give them the stdin and stdout of a session that
// was asked to run "scp -t" or "scp -f".
//
// Only single regular files are supported. There is no recursion and no directory
// transfer; timestamp lines (sent by "scp -p") are accepted and ignored, never sent.
package scp
