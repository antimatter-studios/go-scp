package scp

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// sentFile is what a sending scp puts on the wire for one file: a control line, the body, and its
// own trailing status byte.
func sentFile(control, body string) io.Reader {
	return strings.NewReader(control + "\n" + body + "\x00")
}

func TestReceiveAcceptsAFile(t *testing.T) {
	var out bytes.Buffer
	var got []byte
	var header Header

	err := Receive(sentFile("C0640 11 report.txt", "hello world"), &out, 0, func(h Header, body io.Reader) error {
		header = h
		var err error
		got, err = io.ReadAll(body)
		return err
	})

	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("body = %q", got)
	}
	if header != (Header{Mode: 0o640, Size: 11, Name: "report.txt"}) {
		t.Errorf("header = %+v", header)
	}
	if out.String() != "\x00\x00\x00" {
		t.Errorf("replies = %q, want ready, header accepted, stored", out.String())
	}
}

func TestReceiveTellsTheSenderWhenAcceptFails(t *testing.T) {
	var out bytes.Buffer
	cause := errors.New("disk full")

	err := Receive(sentFile("C0644 5 report.txt", "abcde"), &out, 0, func(Header, io.Reader) error {
		return cause
	})

	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want the accept error", err)
	}
	if out.String() != "\x00\x00\x01disk full\n" {
		t.Errorf("replies = %q, want a failure the sender cannot take for success", out.String())
	}
}

func TestReceiveSendsARejectionsMessageAndReturnsItsError(t *testing.T) {
	var out bytes.Buffer
	cause := errors.New("internal detail")

	err := Receive(sentFile("C0644 1 report.txt", "x"), &out, 0, func(Header, io.Reader) error {
		return &Rejection{Message: "storage unavailable", Err: cause}
	})

	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want it to unwrap to the cause", err)
	}
	if !strings.HasSuffix(out.String(), "\x01storage unavailable\n") || strings.Contains(out.String(), "internal detail") {
		t.Errorf("replies = %q, want only the rejection's message", out.String())
	}
}

func TestReceiveDrainsWhatAcceptLeavesUnread(t *testing.T) {
	var out bytes.Buffer

	err := Receive(sentFile("C0644 5 report.txt", "abcde"), &out, 0, func(Header, io.Reader) error {
		return nil
	})

	if err != nil || out.String() != "\x00\x00\x00" {
		t.Fatalf("Receive = %v, replies %q; the trailing status byte must not be read as content", err, out.String())
	}
}

func TestReceiveRefusesAFileOverMaxSize(t *testing.T) {
	var out bytes.Buffer
	called := false

	err := Receive(sentFile("C0644 4096 report.txt", "x"), &out, 1024, func(Header, io.Reader) error {
		called = true
		return nil
	})

	if err == nil || called {
		t.Fatalf("Receive = %v, accept called %v; want refusal before accept", err, called)
	}
	if out.String() != "\x00\x01report.txt: file too large\n" {
		t.Errorf("replies = %q", out.String())
	}
}

func TestReceiveAcknowledgesTimestampLines(t *testing.T) {
	var out bytes.Buffer
	var got []byte

	in := strings.NewReader("T1700000000 0 1700000000 0\nC0644 2 report.txt\nhi\x00")
	err := Receive(in, &out, 0, func(_ Header, body io.Reader) error {
		var err error
		got, err = io.ReadAll(body)
		return err
	})

	if err != nil || string(got) != "hi" {
		t.Fatalf("Receive = %v, body %q", err, got)
	}
	if out.String() != "\x00\x00\x00\x00" {
		t.Errorf("replies = %q, want the timestamp line acknowledged too", out.String())
	}
}

func TestReceiveRefusesDirectories(t *testing.T) {
	var out bytes.Buffer

	err := Receive(strings.NewReader("D0755 0 somedir\n"), &out, 0, func(Header, io.Reader) error { return nil })

	if !errors.Is(err, ErrProtocol) || !strings.Contains(out.String(), "\x01") {
		t.Fatalf("Receive = %v, replies %q", err, out.String())
	}
}

func TestReceiveRefusesMalformedControlLines(t *testing.T) {
	for _, control := range []string{"C0644 notanumber report.txt", "C0644 -1 report.txt", "C0999 1 report.txt", "C0644 1", "X", ""} {
		var out bytes.Buffer
		err := Receive(strings.NewReader(control+"\n"), &out, 0, func(Header, io.Reader) error { return nil })
		if !errors.Is(err, ErrProtocol) {
			t.Errorf("Receive(%q) = %v, want ErrProtocol", control, err)
		}
		if !strings.Contains(out.String(), "\x01") {
			t.Errorf("Receive(%q) replies %q, want a failure", control, out.String())
		}
	}
}

func TestReceiveKeepsNamesContainingSpaces(t *testing.T) {
	var header Header

	err := Receive(sentFile("C0644 1 my report.txt", "x"), io.Discard, 0, func(h Header, body io.Reader) error {
		header = h
		_, err := io.ReadAll(body)
		return err
	})

	if err != nil || header.Name != "my report.txt" {
		t.Fatalf("Receive = %v, name %q", err, header.Name)
	}
}

func TestReceiveReturnsTheSendersOwnFailure(t *testing.T) {
	var out bytes.Buffer

	err := Receive(strings.NewReader("\x01scp: report.txt: Permission denied\n"), &out, 0, func(Header, io.Reader) error { return nil })

	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Message != "scp: report.txt: Permission denied" {
		t.Fatalf("err = %v, want the sender's message", err)
	}
	if out.String() != "\x00" {
		t.Errorf("replies = %q, want nothing after ready: the sender expects no answer", out.String())
	}
}

func TestSendWritesTheFile(t *testing.T) {
	var out bytes.Buffer

	err := Send(strings.NewReader("\x00\x00\x00"), &out, Header{Mode: 0o644, Size: 6, Name: "notes.txt"}, strings.NewReader("hello\n"))

	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if out.String() != "C0644 6 notes.txt\nhello\n\x00" {
		t.Errorf("wire = %q", out.String())
	}
}

func TestSendWritesAnEmptyFile(t *testing.T) {
	var out bytes.Buffer

	err := Send(strings.NewReader("\x00\x00\x00"), &out, Header{Mode: 0o644, Name: "empty"}, strings.NewReader(""))

	if err != nil || out.String() != "C0644 0 empty\n\x00" {
		t.Fatalf("Send = %v, wire %q", err, out.String())
	}
}

func TestSendStopsWhenThePeerReportsAFailure(t *testing.T) {
	err := Send(strings.NewReader("\x00\x01no space left\n"), io.Discard, Header{Mode: 0o644, Size: 1, Name: "notes.txt"}, strings.NewReader("x"))

	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Message != "no space left" {
		t.Fatalf("err = %v, want the peer's message", err)
	}
}

func TestSendPadsAShortBodyAndReportsTheFailure(t *testing.T) {
	var out bytes.Buffer
	body := iotest.TimeoutReader(strings.NewReader("abc")) // a read error after the first read

	err := Send(strings.NewReader("\x00\x00"), &out, Header{Mode: 0o644, Size: 6, Name: "notes.txt"}, body)

	if err == nil {
		t.Fatal("a body that fails must fail the transfer")
	}
	wire := out.String()
	if !strings.HasPrefix(wire, "C0644 6 notes.txt\nabc\x00\x00\x00\x01scp: notes.txt: ") {
		t.Errorf("wire = %q, want the promised size padded, then a failure", wire)
	}
}

func TestSendRefusesANameThatIsAPath(t *testing.T) {
	for _, name := range []string{"", ".", "..", "dir/file", "two\nlines"} {
		if err := Send(strings.NewReader("\x00\x00\x00"), io.Discard, Header{Name: name}, strings.NewReader("")); err == nil {
			t.Errorf("Send with name %q succeeded", name)
		}
	}
}

func TestSendFailureAnswersAFetchItCannotServe(t *testing.T) {
	var out bytes.Buffer

	if err := SendFailure(strings.NewReader("\x00"), &out, "notes.txt: No such file or directory"); err != nil {
		t.Fatalf("SendFailure: %v", err)
	}
	if out.String() != "\x01notes.txt: No such file or directory\n" {
		t.Errorf("wire = %q", out.String())
	}
}
