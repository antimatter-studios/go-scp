package scp

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want Command
	}{
		{"scp -t /tmp/x", Command{Sink: true, Paths: []string{"/tmp/x"}, Patterns: []bool{false}}},
		{"scp -f /tmp/x", Command{Paths: []string{"/tmp/x"}, Patterns: []bool{false}}},
		{"/usr/bin/scp -v -r -p -d -t -- /tmp/dir", Command{Sink: true, Recursive: true, Preserve: true, TargetIsDir: true, Paths: []string{"/tmp/dir"}, Patterns: []bool{false}}},
		{"scp -pf a b", Command{Preserve: true, Paths: []string{"a", "b"}, Patterns: []bool{false, false}}},
		{`scp -f 'it'\''s a dir/x' "double \"quoted\"" back\ slash`, Command{Paths: []string{"it's a dir/x", `double "quoted"`, "back slash"}, Patterns: []bool{false, false, false}}},
		{"scp -f logs/*.txt 'quoted*.txt' a?c [ab]c", Command{Paths: []string{"logs/*.txt", "quoted*.txt", "a?c", "[ab]c"}, Patterns: []bool{true, false, true, true}}},
		{"scp -t -- -leading-dash", Command{Sink: true, Paths: []string{"-leading-dash"}, Patterns: []bool{false}}},
		// OpenSSH's scp client sends remote paths unquoted for the remote shell,
		// which turns a leading ~ into the login directory, where the session starts.
		{"scp -f ~/notes.txt ~ ~/", Command{Paths: []string{"notes.txt", ".", "."}, Patterns: []bool{false, false, false}}},
		{"scp -t ~/logs", Command{Sink: true, Paths: []string{"logs"}, Patterns: []bool{false}}},
		{`scp -f '~/x' a~b ~other/x \~/y`, Command{Paths: []string{"~/x", "a~b", "~other/x", "~/y"}, Patterns: []bool{false, false, false, false}}},
	}
	for _, c := range cases {
		got, err := ParseCommand(c.cmd)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseCommand(%q) = (%+v, %v), want %+v", c.cmd, got, err, c.want)
		}
	}
}

func TestParseCommandRefuses(t *testing.T) {
	for _, cmd := range []string{
		"",
		"ls -l",
		"scp /tmp/x",
		"scp -t",
		"scp -t a b",
		"scp -x -t a",
		"scp -t a; touch b",
		"scp -t a | cat",
		"scp -t a && b",
		"scp -t $(b)",
		"scp -t `b`",
		`scp -t "$(b)"`,
		"scp -t 'unterminated",
		`scp -t "unterminated`,
		"scp -t a > b",
	} {
		if got, err := ParseCommand(cmd); err == nil {
			t.Errorf("ParseCommand(%q) = %+v, want an error", cmd, got)
		}
	}
}

func TestCommandExpand(t *testing.T) {
	fsys := fstest.MapFS{
		"logs/a.txt": {}, "logs/b.txt": {}, "logs/c.log": {}, "notes": {},
	}
	c := Command{Paths: []string{"logs/*.txt", "notes", "none/*.txt", "*.txt"}, Patterns: []bool{true, false, true, false}}

	got, err := c.Expand(fsys)

	want := []string{"logs/a.txt", "logs/b.txt", "notes", "none/*.txt", "*.txt"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Expand = (%v, %v), want %v", got, err, want)
	}
}

func TestCommandExpandReportsABadPattern(t *testing.T) {
	c := Command{Paths: []string{"[unclosed"}, Patterns: []bool{true}}

	if _, err := c.Expand(fstest.MapFS{}); err == nil {
		t.Fatal("a malformed pattern must be an error")
	}
}

func TestQuotePattern(t *testing.T) {
	cases := map[string]string{
		"logs/*.txt":   `'logs/'*'.txt'`,
		"a?c":          `'a'?'c'`,
		"[ab]c":        `[ab]'c'`,
		"x[y":          `'x[y'`,
		"it's/*":       `'it'\''s/'*`,
		"plain":        `'plain'`,
		"*":            `*`,
		"dir/[!.]*.go": `'dir/'[!.]*'.go'`,
	}
	for in, want := range cases {
		if got := quotePattern(in); got != want {
			t.Errorf("quotePattern(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestCommandRelative(t *testing.T) {
	c := Command{
		Paths:    []string{"/srv/data", "../../up/and/over", "a/../../b", "", "/", "plain", "logs/*.txt"},
		Patterns: []bool{false, false, false, false, false, false, true},
	}

	got := c.Relative()

	want := []string{"srv/data", "up/and/over", "b", ".", ".", "plain", "logs/*.txt"}
	if !reflect.DeepEqual(got.Paths, want) || !reflect.DeepEqual(got.Patterns, c.Patterns) {
		t.Fatalf("Relative = %v %v, want %v", got.Paths, got.Patterns, want)
	}
	if c.Paths[0] != "/srv/data" {
		t.Error("Relative changed the original command")
	}
}
