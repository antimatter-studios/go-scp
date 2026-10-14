//go:build !windows

package scp

// nameBreaks are the characters that make a name more than one file name here: a
// directory separator, and a line break, which would end a control line.
const nameBreaks = "/\n"
