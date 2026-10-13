package scp

import (
	"errors"
	"strings"
	"testing"
)

func TestParseHeader(t *testing.T) {
	cases := []struct {
		line string
		want Header
	}{
		{"C0644 12 notes.txt", Header{Mode: 0o644, Size: 12, Name: "notes.txt"}},
		{"C0600 0 empty", Header{Mode: 0o600, Size: 0, Name: "empty"}},
		{"C4755 1 setuid keeps only permission bits", Header{Mode: 0o755, Size: 1, Name: "setuid keeps only permission bits"}},
	}
	for _, c := range cases {
		got, err := parseHeader(c.line)
		if err != nil || got != c.want {
			t.Errorf("parseHeader(%q) = (%+v, %v), want %+v", c.line, got, err, c.want)
		}
	}
}

func TestParseHeaderRefusesWhatIsNotAControlLine(t *testing.T) {
	for _, line := range []string{"", "C", "C0644", "C0644 1", "C0644 1 ", "C0844 1 x", "C77777 1 x", "C0644 x name", "C0644 -5 name", "D0755 0 dir"} {
		if _, err := parseHeader(line); !errors.Is(err, ErrProtocol) {
			t.Errorf("parseHeader(%q) = %v, want ErrProtocol", line, err)
		}
	}
}

func TestHeaderStringRoundTrips(t *testing.T) {
	h := Header{Mode: 0o640, Size: 42, Name: "a name with spaces"}

	got, err := parseHeader(h.String())

	if err != nil || got != h || h.String() != "C0640 42 a name with spaces" {
		t.Fatalf("String = %q, parsed back as (%+v, %v)", h.String(), got, err)
	}
}

func TestQuoteReadsBackAsTheOriginal(t *testing.T) {
	for _, s := range []string{"plain", "with space", "it's", "'", `$(x) ` + "`y`" + ` "z" \n`} {
		got := Quote(s)
		if !strings.HasPrefix(got, "'") || !strings.HasSuffix(got, "'") {
			t.Errorf("Quote(%q) = %q, want one single-quoted word", s, got)
		}
		if strings.ReplaceAll(got[1:len(got)-1], `'\''`, `'`) != s {
			t.Errorf("Quote(%q) = %q does not read back", s, got)
		}
	}
}

func FuzzParseHeader(f *testing.F) {
	for _, seed := range []string{"C0644 12 notes.txt", "C0600 0 x", "C0644 -1 x", "C", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		h, err := parseHeader(line)
		if err != nil {
			return
		}
		again, err := parseHeader(h.String())
		if err != nil || again != h {
			t.Fatalf("%q parsed as %+v, which does not round-trip: (%+v, %v)", line, h, again, err)
		}
	})
}
