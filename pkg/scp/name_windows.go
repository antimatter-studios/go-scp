package scp

// nameBreaks are the characters that make a name more than one file name here: a
// line break, which would end a control line, and on Windows a backslash or a
// slash, which separate directories, or a colon, which names a drive.
const nameBreaks = "/\n\\:"
