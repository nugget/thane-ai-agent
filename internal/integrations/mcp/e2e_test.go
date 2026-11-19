package mcp

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// End-to-end tests drive the real serverClient over the real stdio and
// Streamable HTTP transports (e2e_stdio_test.go, e2e_http_test.go),
// against helper-process and httptest fake servers. The unit tests of
// the client and of each transport use scripted fakes for the other
// side; these prove the pieces agree on the wire.

// e2eTWait bounds every wait in the end-to-end tests. Real processes
// under -race start slowly, so it is generous.
const e2eTWait = 20 * time.Second

// e2eTConfig returns a client configuration for real transports: fast
// backoff, and a probe timeout no healthy fake ever hits.
func e2eTConfig(transport, protocol string) clientConfig {
	cfg := clientTConfig(transport, protocol)
	cfg.connectTimeout = e2eTWait
	cfg.callTimeout = e2eTWait
	cfg.probeTimeout = 10 * time.Second
	return cfg
}

// e2eTNew builds a client on factory f (not started). It is closed when
// the test ends.
func e2eTNew(t *testing.T, cfg clientConfig, f transportFactory) *serverClient {
	t.Helper()
	if cfg.logger == nil {
		cfg.logger = coreTLogger(t)
	}
	c := newServerClient(t.Context(), cfg, f)
	c.debounce = time.Millisecond
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), e2eTWait)
		defer cancel()
		if err := c.close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return c
}

// e2eTStart starts c and waits for its first connect attempt.
func e2eTStart(t *testing.T, c *serverClient) {
	t.Helper()
	c.start()
	select {
	case <-c.firstAttempt():
	case <-time.After(e2eTWait):
		t.Fatal("first connect attempt did not finish")
	}
}

// e2eTEventually polls cond until it holds or e2eTWait passes.
func e2eTEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	coreTWaitFor(t, e2eTWait, what, cond)
}

// e2eTReady waits until c is ready and returns its connection.
func e2eTReady(t *testing.T, c *serverClient) *clientConn {
	t.Helper()
	var conn *clientConn
	var last error
	deadline := time.Now().Add(e2eTWait)
	for {
		cc, err := c.readyConn()
		if err == nil {
			conn = cc
			break
		}
		last = err
		if time.Now().After(deadline) {
			t.Fatalf("client never became ready: %v", last)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return conn
}

// e2eTCheckConn asserts the negotiated era and version.
func e2eTCheckConn(t *testing.T, conn *clientConn, wantEra era, wantVersion string) {
	t.Helper()
	if got := conn.currentEra(); got != wantEra {
		t.Errorf("era = %s, want %s", got, wantEra)
	}
	if conn.version != wantVersion {
		t.Errorf("version = %q, want %q", conn.version, wantVersion)
	}
}

// e2eTCheckTools asserts the published tool names, in server order.
func e2eTCheckTools(t *testing.T, c *serverClient, want ...string) {
	t.Helper()
	if got := clientTToolNames(c); !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
}

// e2eTEcho calls the fake servers' echo tool and checks its text.
func e2eTEcho(t *testing.T, c *serverClient, text string) {
	t.Helper()
	res, err := c.callTool(t.Context(), "echo", map[string]any{"text": text})
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if res.IsError || res.Text != text {
		t.Fatalf("echo = %+v, want text %q", res, text)
	}
}

// e2eTModelText returns the model-facing text the bridge would show for
// a callTool error.
func e2eTModelText(t *testing.T, err error) string {
	t.Helper()
	var mf *promptfmt.ModelFacingError
	if !errors.As(modelFacingCallError("mcp_fake_tool", err), &mf) {
		t.Fatalf("modelFacingCallError(%v) is not a model-facing error", err)
	}
	return mf.Text
}
