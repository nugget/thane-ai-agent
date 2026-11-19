package mcp

import (
	"context"
	"fmt"
	"os"
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

// Transports sharing exitWarned log a run of failures at WARN once and
// the repeats at Debug, whether a process died at start-up or after it
// spoke JSON-RPC: a server that answers and then crashes is still
// failing. Only clearing exitWarned, which the client does once a
// connection has lived stableAfter, starts a new run.
func TestStdio_ExitWarnsOncePerRun(t *testing.T) {
	var warned atomic.Bool
	logs := &coreTLogBuffer{}
	run := func(mode string, spoke bool) {
		t.Helper()
		cfg := stdioTHelper(t, mode)
		cfg.logger = logs.logger()
		tr := stdioTStart(t, cfg, transportHooks{exitWarned: &warned})
		if spoke {
			stdioTMust(t, tr, "echo", nil, nil)
			_, _ = stdioTCall(t, tr, "crash", nil, callInfo{})
		}
		stdioTWaitDone(t, tr.Done(), 10*time.Second, "Done")
		stdioTEventually(t, "exit logged", func() bool { return strings.Contains(logs.String(), "pid="+strconv.Itoa(tr.pid)+" exit=") })
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
	if n := stdioTExitWarns(logs.String()); n != 1 {
		t.Fatalf("a crash right after the server spoke logged %d exit WARNs in all, want 1 (same run of failures):\n%s", n, logs.String())
	}
	warned.Store(false) // the client's stableAfter point
	run("stdio-script", true)
	if n := stdioTExitWarns(logs.String()); n != 2 {
		t.Fatalf("a crash after the run was re-armed logged %d exit WARNs in all, want 2:\n%s", n, logs.String())
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

const (
	// stdioTModeFlap is the "stdio-flap" helper mode (stdioTHelper).
	stdioTModeFlap = "stdio-flap"
	// stdioTFlapAfterEnv delays the "stdio-flap" exit by this many
	// milliseconds after the last tools/list page.
	stdioTFlapAfterEnv = "MCP_TEST_FLAP_AFTER_MS"
	// stdioTFlapStderr is what a "stdio-flap" helper says as it exits.
	stdioTFlapStderr = "flap: crashing after serving tools/list"
)

// stdioTFlapExit ends a "stdio-flap" helper as a crashing server would.
func stdioTFlapExit() {
	if ms, err := strconv.Atoi(os.Getenv(stdioTFlapAfterEnv)); err == nil && ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, stdioTFlapStderr)
	os.Exit(1)
}

// stdioTFlapRun runs a client through the Manager's stdio factory on a
// legacy "stdio-flap" server that crashes afterMS after serving its tool
// list, until the server has connected and crashed cycles times. It
// closes the client and returns the logs.
func stdioTFlapRun(t *testing.T, afterMS int, stableAfter time.Duration, cycles int) string {
	t.Helper()
	h := stdioTHelper(t, stdioTModeFlap)
	srv := ServerConfig{Name: "flap", Transport: transportStdio, Protocol: protocolLegacy,
		Command: h.command, Args: h.args, Env: append(h.env, stdioTFlapAfterEnv+"="+strconv.Itoa(afterMS))}
	logs := &coreTLogBuffer{}
	m := NewManager(nil, logs.logger())
	cfg := clientTConfig(transportStdio, protocolLegacy)
	cfg.backoffMin, cfg.backoffMax = 10*time.Millisecond, 20*time.Millisecond
	cfg.stableAfter = stableAfter
	cfg.logger = logs.logger()
	c := e2eTNew(t, cfg, m.factoryFor(srv, logs.logger()))
	c.start()
	e2eTEventually(t, fmt.Sprintf("%d connects and crashes", cycles), func() bool {
		out := logs.String()
		return strings.Count(out, `msg="mcp: server connected"`) >= cycles &&
			strings.Count(out, "exited unexpectedly") >= cycles
	})
	ctx, cancel := context.WithTimeout(t.Context(), e2eTWait)
	defer cancel()
	if err := c.close(ctx); err != nil {
		t.Fatal(err)
	}
	return logs.String()
}

// A server that answers the handshake and its tool list, then crashes,
// flaps: the supervisor respawns it every backoff. Its exit and stderr
// tail are logged at WARN once per run of failures, like the lost
// connection, not on every respawn; a connection that lived stableAfter
// before the crash ends the run, so that crash warns again.
func TestStdio_FlappingServerExitWarnsOncePerRun(t *testing.T) {
	t.Run("never stable", func(t *testing.T) {
		out := stdioTFlapRun(t, 0, time.Hour, 5)
		if n := stdioTExitWarns(out); n != 1 {
			t.Fatalf("logged %d exit WARNs over 5 connect-and-crash cycles, want 1:\n%s", n, out)
		}
		if n := strings.Count(out, `level=WARN msg="mcp: server connection lost; reconnecting"`); n != 1 {
			t.Errorf("logged %d connection-lost WARNs, want 1", n)
		}
		for line := range strings.SplitSeq(out, "\n") {
			if stdioTExitWarns(line) == 1 && !strings.Contains(line, stdioTFlapStderr) {
				t.Errorf("the WARN lacks the stderr tail: %s", line)
			}
		}
	})
	t.Run("stable between crashes", func(t *testing.T) {
		out := stdioTFlapRun(t, 400, 50*time.Millisecond, 3)
		crashes := strings.Count(out, "exited unexpectedly")
		if n := stdioTExitWarns(out); n != crashes {
			t.Fatalf("logged %d exit WARNs for %d crashes after a stable stretch, want one each:\n%s", n, crashes, out)
		}
		// Each loss after a stable stretch is a new run, so it warns too:
		// the connections did become stable between the crashes.
		if n := strings.Count(out, `level=WARN msg="mcp: server connection lost; reconnecting"`); n < 3 {
			t.Errorf("logged %d connection-lost WARNs, want one per crash (at least 3):\n%s", n, out)
		}
	})
}
