package mcp

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stdioTExitWarns counts the exit WARNs in log output.
func stdioTExitWarns(out string) int {
	n := 0
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "level=WARN") &&
			(strings.Contains(line, "exited unexpectedly") || strings.Contains(line, "closed before it sent any JSON-RPC message")) {
			n++
		}
	}
	return n
}

// Transports sharing exitWarned log a run of start-up failures at WARN
// once, the repeats at Debug, and warn again after a process that came
// up (sent a valid message) exits.
func TestStdio_ExitWarnsOncePerRun(t *testing.T) {
	var warned atomic.Bool
	logs := &coreTLogBuffer{}
	run := func(mode string, spoke bool) *stdioTransport {
		t.Helper()
		cfg := stdioTHelper(t, mode)
		cfg.logger = logs.logger()
		cfg.exitWarned = &warned
		tr := stdioTStart(t, cfg, transportHooks{})
		if spoke {
			stdioTMust(t, tr, "echo", nil, nil)
			_, _ = stdioTCall(t, tr, "crash", nil, callInfo{})
		}
		stdioTWaitDone(t, tr.Done(), 10*time.Second, "Done")
		stdioTEventually(t, "exit logged", func() bool { return strings.Contains(logs.String(), "pid="+strconv.Itoa(tr.pid)+" exit=") })
		return tr
	}

	run("stdio-silent-exit", false)
	run("stdio-silent-exit", false)
	if n := stdioTExitWarns(logs.String()); n != 1 {
		t.Fatalf("two start-up failures logged %d exit WARNs, want 1:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "level=DEBUG msg=\"mcp: stdio server exited unexpectedly\"") {
		t.Errorf("the repeat is not at Debug:\n%s", logs.String())
	}
	run("stdio-script", true)
	if n := stdioTExitWarns(logs.String()); n != 2 {
		t.Fatalf("a crash after the server came up logged %d exit WARNs in all, want 2:\n%s", n, logs.String())
	}
	run("stdio-silent-exit", false)
	if n := stdioTExitWarns(logs.String()); n != 2 {
		t.Errorf("the start-up failure after the crash logged a WARN again:\n%s", logs.String())
	}
}

// Through the Manager's factory, a stdio server that dies at start-up on
// every attempt logs its exit and stderr tail at WARN once, however
// often the supervisor retries it.
func TestStdio_StartupFailureWarnsOnceThroughManager(t *testing.T) {
	logs := &coreTLogBuffer{}
	m := NewManager(nil, logs.logger())
	f := m.factoryFor(ServerConfig{Name: "gh", Transport: transportStdio, Protocol: protocolAuto,
		Command: "/bin/sh", Args: []string{"-c", "echo 'fatal: GITHUB_TOKEN not set' >&2; exit 1"}}, logs.logger())
	cfg := clientTConfig(transportStdio, protocolAuto)
	cfg.backoffMin, cfg.backoffMax = 10*time.Millisecond, 20*time.Millisecond
	cfg.logger = logs.logger()
	c := e2eTNew(t, cfg, f)
	c.start()
	e2eTEventually(t, "several failed attempts", func() bool {
		return strings.Count(logs.String(), "stdio server started") >= 6 && stdioTExitLines(logs.String()) >= 6
	})
	ctx, cancel := context.WithTimeout(t.Context(), e2eTWait)
	defer cancel()
	if err := c.close(ctx); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if n := stdioTExitWarns(out); n != 1 {
		t.Fatalf("logged %d exit WARNs, want 1:\n%s", n, out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if stdioTExitWarns(line) == 1 && !strings.Contains(line, "fatal: GITHUB_TOKEN not set") {
			t.Errorf("the WARN lacks the stderr tail: %s", line)
		}
	}
}

// stdioTExitLines counts exit lines at any level.
func stdioTExitLines(out string) int {
	return strings.Count(out, "exited unexpectedly") + strings.Count(out, "closed before it sent any JSON-RPC message")
}
