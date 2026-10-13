package scp

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCopyOnHostRunsScpOnTheConnectedHost(t *testing.T) {
	var got string
	c := newTestServer(t, func(cmd string, _ io.Reader, _, _ io.Writer) int {
		got = cmd
		return 0
	}).dial(t)
	c.Recursive, c.Preserve = true, true

	if err := c.CopyOnHost(t.Context(), "host-b:/srv/it's here", "/data/a b", "/data/c"); err != nil {
		t.Fatalf("CopyOnHost: %v", err)
	}
	want := `scp -p -r -q '/data/a b' '/data/c' 'host-b:/srv/it'\''s here'`
	if got != want {
		t.Errorf("command = %s\nwant      %s", got, want)
	}
}

func TestCopyOnHostLeavesWildcardsForTheHostsShell(t *testing.T) {
	var got string
	c := newTestServer(t, func(cmd string, _ io.Reader, _, _ io.Writer) int {
		got = cmd
		return 0
	}).dial(t)
	c.Glob = true

	if err := c.CopyOnHost(t.Context(), "host-b:/srv", "/data/*.log"); err != nil {
		t.Fatalf("CopyOnHost: %v", err)
	}
	if got != `scp -q '/data/'*'.log' 'host-b:/srv'` {
		t.Errorf("command = %s", got)
	}
}

func TestCopyOnHostReportsTheHostsReason(t *testing.T) {
	c := newTestServer(t, func(_ string, _ io.Reader, _, stderr io.Writer) int {
		io.WriteString(stderr, "ssh: Could not resolve hostname host-c\n")
		return 1
	}).dial(t)

	err := c.CopyOnHost(t.Context(), "host-c:/srv", "/data/a")

	if err == nil || !strings.Contains(err.Error(), "Could not resolve hostname host-c") {
		t.Fatalf("err = %v, want the host's own reason", err)
	}
}

func TestCopyOnHostNeedsSourcesAndALiveContext(t *testing.T) {
	c := newTestServer(t, func(string, io.Reader, io.Writer, io.Writer) int { return 0 }).dial(t)

	if err := c.CopyOnHost(t.Context(), "host-b:/srv"); err == nil {
		t.Error("no sources must be an error")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.CopyOnHost(ctx, "host-b:/srv", "/data/a"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
