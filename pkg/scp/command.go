package scp

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// Command is an scp command line as a client sends it to the remote end: "scp -t"
// to send files there, "scp -f" to fetch them. An SSH server that handles
// transfers itself parses the command a session asks to run with ParseCommand,
// then serves it with ReceiveAll or SendFS.
type Command struct {
	// Sink is set for "scp -t": the peer sends files. Otherwise the peer fetches
	// them ("scp -f").
	Sink bool

	Recursive   bool // -r
	Preserve    bool // -p
	TargetIsDir bool // -d: the target must be a directory

	// Paths are the operands with their quoting removed. For -t there is exactly
	// one, the target.
	Paths []string

	// Patterns marks the paths that held wildcards outside quotes. A shell would
	// have expanded them; a server should do the same, for example with Expand.
	Patterns []bool
}

// ParseCommand reads an scp command line. Quoting follows the POSIX shell, single
// and double quotes and backslashes, since that is how a client quotes paths for
// the remote end. An unquoted ~ that starts a path is the login directory, where
// the session starts, so ~/x reads as x. Anything a shell would treat as more than
// one plain command, such as ; | & < > or a substitution, is refused.
func ParseCommand(cmd string) (Command, error) {
	words, patterns, err := splitWords(cmd)
	if err != nil {
		return Command{}, err
	}
	if len(words) == 0 || path.Base(words[0]) != "scp" {
		return Command{}, fmt.Errorf("scp: %q is not an scp command", cmd)
	}

	var c Command
	direction := false
	i := 1
	for ; i < len(words); i++ {
		w := words[i]
		if w == "--" {
			i++
			break
		}
		if !strings.HasPrefix(w, "-") || w == "-" || patterns[i] {
			break
		}
		for _, flag := range w[1:] {
			switch flag {
			case 't':
				c.Sink, direction = true, true
			case 'f':
				direction = true
			case 'r':
				c.Recursive = true
			case 'p':
				c.Preserve = true
			case 'd':
				c.TargetIsDir = true
			case 'v', 'q':
				// Verbosity: nothing to do.
			default:
				return Command{}, fmt.Errorf("scp: unsupported option -%c in %q", flag, cmd)
			}
		}
	}
	if !direction {
		return Command{}, fmt.Errorf("scp: %q has neither -t nor -f", cmd)
	}
	c.Paths, c.Patterns = words[i:], patterns[i:]
	switch {
	case len(c.Paths) == 0:
		return Command{}, fmt.Errorf("scp: %q names no path", cmd)
	case c.Sink && len(c.Paths) != 1:
		return Command{}, fmt.Errorf("scp: %q names more than one target", cmd)
	}
	return c, nil
}

// Relative returns c with every path made relative and clean: a leading / and any
// .. that would climb above the start are dropped, so each path names something
// under whatever root the server joins it to. An empty path becomes ".". A path
// that is valid UTF-8 is then valid in an fs.FS; one that is not can still be used
// through an os.Root, as DirSink does, but no fs.FS can open it.
func (c Command) Relative() Command {
	paths := make([]string, len(c.Paths))
	for i, p := range c.Paths {
		rel := strings.TrimPrefix(path.Clean("/"+p), "/")
		if rel == "" {
			rel = "."
		}
		paths[i] = rel
	}
	c.Paths = paths
	return c
}

// Expand returns the files a fetch should send from fsys: each path as it is, or,
// where the command held a wildcard, every match in sorted order. A pattern that
// matches nothing is kept, so the peer is told it does not exist.
func (c Command) Expand(fsys fs.FS) ([]string, error) {
	var names []string
	for i, p := range c.Paths {
		if !c.Patterns[i] {
			names = append(names, p)
			continue
		}
		matches, err := fs.Glob(fsys, p)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			matches = []string{p}
		}
		names = append(names, matches...)
	}
	return names, nil
}

// Characters with a meaning to a POSIX shell, as splitWords reads a command.
const (
	// wordSeparators end a word outside quotes.
	wordSeparators = " \t\n"
	// shellOperators would make a command more than one plain command, or ask the
	// shell for a substitution.
	shellOperators = ";|&<>()$`"
	// doubleQuoteEscapes are the characters a backslash escapes inside double
	// quotes; before any other character it stays as it is.
	doubleQuoteEscapes = "\\\"$`\n"
	// wildcards start a pattern for the shell to expand, outside quotes.
	wildcards = "*?["
)

// splitWords splits cmd into words as a POSIX shell would, and reports which words
// held a wildcard outside quotes.
func splitWords(cmd string) (words []string, patterns []bool, err error) {
	var word strings.Builder
	inWord, pattern := false, false
	end := func() {
		if inWord {
			words = append(words, word.String())
			patterns = append(patterns, pattern)
		}
		word.Reset()
		inWord, pattern = false, false
	}

	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		switch {
		case strings.IndexByte(wordSeparators, ch) >= 0:
			end()
		case ch == '\'', ch == '"':
			quoted, closing, err := readQuoted(cmd, i)
			if err != nil {
				return nil, nil, err
			}
			word.WriteString(quoted)
			inWord, i = true, closing
		case ch == '\\':
			if i+1 < len(cmd) {
				i++
				word.WriteByte(cmd[i])
				inWord = true
			}
		case strings.IndexByte(shellOperators, ch) >= 0:
			return nil, nil, fmt.Errorf("scp: %q is more than a plain scp command", cmd)
		case ch == '~' && !inWord && startsLoginDirectory(cmd, i):
			// An unquoted ~ that starts a word is the login directory, which is
			// where an scp session starts, so ~/x is x and ~ is ".", as the shell
			// would have it. OpenSSH's scp client sends such paths unquoted.
			inWord = true
			if i+1 < len(cmd) && cmd[i+1] == '/' {
				i++
			}
			if wordEndsAt(cmd, i+1) {
				word.WriteByte('.')
			}
		default:
			pattern = pattern || strings.IndexByte(wildcards, ch) >= 0
			word.WriteByte(ch)
			inWord = true
		}
	}
	end()
	return words, patterns, nil
}

// wordEndsAt reports whether the word being read ends before cmd[i].
func wordEndsAt(cmd string, i int) bool {
	return i == len(cmd) || strings.IndexByte(wordSeparators, cmd[i]) >= 0
}

// startsLoginDirectory reports whether the ~ at cmd[i] stands alone or starts a
// path, rather than being the start of a name such as ~user.
func startsLoginDirectory(cmd string, i int) bool {
	return wordEndsAt(cmd, i+1) || cmd[i+1] == '/'
}

// readQuoted reads the quoted text that opens at cmd[open], single- or
// double-quoted, and returns it as the shell would read it, with the index of the
// closing quote.
func readQuoted(cmd string, open int) (text string, closing int, err error) {
	if cmd[open] == '\'' {
		end := strings.IndexByte(cmd[open+1:], '\'')
		if end < 0 {
			return "", 0, errors.New("scp: unterminated single quote")
		}
		return cmd[open+1 : open+1+end], open + 1 + end, nil
	}
	var b strings.Builder
	for j := open + 1; j < len(cmd); j++ {
		switch ch := cmd[j]; {
		case ch == '"':
			return b.String(), j, nil
		case ch == '$' || ch == '`':
			return "", 0, fmt.Errorf("scp: %q asks the shell for a substitution", cmd)
		case ch == '\\' && j+1 < len(cmd) && strings.IndexByte(doubleQuoteEscapes, cmd[j+1]) >= 0:
			j++
		}
		b.WriteByte(cmd[j])
	}
	return "", 0, errors.New("scp: unterminated double quote")
}
