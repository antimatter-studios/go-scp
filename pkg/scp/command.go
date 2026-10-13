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
// the remote end. Anything a shell would treat as more than one plain command,
// such as ; | & < > or a substitution, is refused.
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
		case ch == ' ' || ch == '\t' || ch == '\n':
			end()
		case ch == '\'':
			closing := strings.IndexByte(cmd[i+1:], '\'')
			if closing < 0 {
				return nil, nil, errors.New("scp: unterminated single quote")
			}
			word.WriteString(cmd[i+1 : i+1+closing])
			inWord = true
			i += closing + 1
		case ch == '"':
			j := i + 1
			for ; j < len(cmd) && cmd[j] != '"'; j++ {
				switch cmd[j] {
				case '\\':
					if j+1 < len(cmd) && strings.IndexByte("\\\"$`\n", cmd[j+1]) >= 0 {
						j++
					}
				case '$', '`':
					return nil, nil, fmt.Errorf("scp: %q asks the shell for a substitution", cmd)
				}
				word.WriteByte(cmd[j])
			}
			if j >= len(cmd) {
				return nil, nil, errors.New("scp: unterminated double quote")
			}
			inWord = true
			i = j
		case ch == '\\':
			if i+1 < len(cmd) {
				i++
				word.WriteByte(cmd[i])
				inWord = true
			}
		case strings.IndexByte(";|&<>()$`", ch) >= 0:
			return nil, nil, fmt.Errorf("scp: %q is more than a plain scp command", cmd)
		default:
			if ch == '*' || ch == '?' || ch == '[' {
				pattern = true
			}
			word.WriteByte(ch)
			inWord = true
		}
	}
	end()
	return words, patterns, nil
}
