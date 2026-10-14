//go:build !windows

package sftp

// nameBreaks are the characters that make a name more than one file name here: a
// directory separator, and a line break.
const nameBreaks = "/\n"
