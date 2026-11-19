package mcp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// e2eTHTTPClient builds (not starts) a client for srv.
func e2eTHTTPClient(t *testing.T, srv *e2eTHTTP, protocol string, tweak func(*clientConfig)) *serverClient {
	t.Helper()
	cfg := e2eTConfig(transportHTTP, protocol)
	if tweak != nil {
		tweak(&cfg)
	}
	return e2eTNew(t, cfg, srv.factory(t, map[string]string{"X-Api-Key": "e2e-secret"}))
}

// e2eTCancelSleep runs the fake's blocking "sleep" tool, cancels it once
// the server holds the POST, and returns the call's error.
func e2eTCancelSleep(t *testing.T, c *serverClient, srv *e2eTHTTP) error {
	t.Helper()
	before := srv.count(methodToolsCall)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var (
		wg  sync.WaitGroup
		err error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err = c.callTool(ctx, "sleep", nil)
	}()
	e2eTEventually(t, "server holding the sleep call", func() bool { return srv.count(methodToolsCall) > before })
	cancel()
	wg.Wait()
	return err
}

// TestE2E_HTTPLegacyStateful drives a legacy stateful server: the
// discover probe's 400 (-32000, null id) means legacy; initialize over
// SSE assigns a session; tool calls stream notifications and server
// requests that Thane answers by POST; a cancelled call is cancelled
// with notifications/cancelled; a 404 for an expired session
// re-initializes and retries the call once (and a health check that
// hits the expiry reconnects); Close ends the live session with DELETE.
// The TypeScript-style variant answers an unknown session with 400 and
// a null-id -32000 body instead of 404, with the same recovery.
func TestE2E_HTTPLegacyStateful(t *testing.T) {
	t.Parallel()
	for _, style := range []string{e2eTStyleStateful, e2eTStyleStatefulTS} {
		t.Run(style, func(t *testing.T) {
			t.Parallel()
			e2eTLegacyStateful(t, style)
		})
	}
}

func e2eTLegacyStateful(t *testing.T, style string) {
	srv := e2eTHTTPStart(t, style)
	c := e2eTHTTPClient(t, srv, protocolAuto, nil)
	e2eTStart(t, c)
	e2eTCheckConn(t, e2eTReady(t, c), eraLegacy, handshakeOfferVersion)
	e2eTCheckTools(t, c, "echo", "sleep")
	if srv.count(methodDiscover) != 1 || srv.count(methodInitialize) != 1 || srv.count(methodInitialized) != 1 {
		t.Errorf("handshake requests: %v", srv.snapshot())
	}

	e2eTEcho(t, c, "over sse")
	if got := srv.count("reply"); got != 2 {
		t.Errorf("client replied to %d server requests, want 2", got)
	}
	if err := c.probe(t.Context()); err != nil {
		t.Fatalf("probe: %v", err)
	}

	err := e2eTCancelSleep(t, c, srv)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call error = %v, want context.Canceled", err)
	}
	e2eTEventually(t, "notifications/cancelled delivered", func() bool { return srv.count(methodCancelled) == 1 })

	// The server restarts and forgets the session: the call re-initializes
	// (straight to initialize: the era is cached) and is retried once.
	srv.expireSessions()
	e2eTEcho(t, c, "after expiry")
	if srv.count(methodInitialize) != 2 || srv.count(methodDiscover) != 1 {
		t.Errorf("after expiry: %v", srv.snapshot())
	}

	// A health check that meets the expiry asks for a reconnect.
	srv.expireSessions()
	if err := c.probe(t.Context()); !errors.Is(err, errSessionExpired) {
		t.Fatalf("probe on an expired session = %v, want errSessionExpired", err)
	}
	e2eTEventually(t, "re-initialized after the probe", func() bool { return c.probe(t.Context()) == nil })
	if srv.count(methodInitialize) != 3 || srv.count(http.MethodDelete) != 0 {
		t.Errorf("after probe expiry: %v", srv.snapshot())
	}

	ctx, cancel := context.WithTimeout(t.Context(), e2eTWait)
	defer cancel()
	if err := c.close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := srv.count(http.MethodDelete); got != 1 {
		t.Errorf("DELETE sent %d times, want 1 (only the live session)", got)
	}
	srv.checkClean(t)
}

// TestE2E_HTTPLegacyStateless: a stateless legacy server answers
// server/discover with HTTP 200 carrying -32601, which means legacy.
// No request ever carries a session id, every request after initialize
// carries MCP-Protocol-Version, and Close sends no DELETE.
func TestE2E_HTTPLegacyStateless(t *testing.T) {
	t.Parallel()
	srv := e2eTHTTPStart(t, e2eTStyleStateless)
	c := e2eTHTTPClient(t, srv, protocolAuto, nil)
	e2eTStart(t, c)
	e2eTCheckConn(t, e2eTReady(t, c), eraLegacy, handshakeOfferVersion)
	e2eTCheckTools(t, c, "echo", "sleep")
	e2eTEcho(t, c, "over json")
	if err := c.probe(t.Context()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if err := e2eTCancelSleep(t, c, srv); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call error = %v, want context.Canceled", err)
	}
	e2eTEventually(t, "notifications/cancelled delivered", func() bool { return srv.count(methodCancelled) == 1 })

	ctx, cancel := context.WithTimeout(t.Context(), e2eTWait)
	defer cancel()
	if err := c.close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if srv.count(methodDiscover) != 1 || srv.count(methodInitialize) != 1 || srv.count(http.MethodDelete) != 0 {
		t.Errorf("requests: %v", srv.snapshot())
	}
	srv.checkClean(t)
}

// TestE2E_HTTPModern drives a modern server that enforces the modern
// headers. Its first server/discover answer is 400 with -32022 naming
// 2026-07-28, so Thane re-probes once; the tool with an invalid
// x-mcp-header annotation is dropped; Mcp-Name and Mcp-Param-Region
// (base64-encoded when not header-safe) match the body; a scripted
// -32020 re-lists and retries once; cancelling aborts the POST without
// notifications/cancelled; no session, DELETE or reply is ever sent.
func TestE2E_HTTPModern(t *testing.T) {
	t.Parallel()
	srv := e2eTHTTPStart(t, e2eTStyleModern)
	srv.script(1, 1)
	c := e2eTHTTPClient(t, srv, protocolAuto, nil)
	e2eTStart(t, c)
	conn := e2eTReady(t, c)
	e2eTCheckConn(t, conn, eraModern, modernProtocolVersion)
	if conn.server.Name != "e2e-modern" {
		t.Errorf("server = %+v", conn.server)
	}
	e2eTCheckTools(t, c, "echo", "sleep", "weather")
	if srv.count(methodDiscover) != 2 || srv.count(methodInitialize) != 0 {
		t.Errorf("negotiation requests: %v", srv.snapshot())
	}

	res, err := c.callTool(t.Context(), "weather", map[string]any{"region": "zürich"})
	if err != nil || res.Text != "weather in zürich" {
		t.Fatalf("weather = %+v, %v", res, err)
	}
	if srv.count(methodToolsCall) != 2 || srv.count(methodToolsList) != 2 {
		t.Errorf("-32020 retry: %v", srv.snapshot())
	}
	res, err = c.callTool(t.Context(), "weather", map[string]any{"region": "eu-west-1"})
	if err != nil || res.Text != "weather in eu-west-1" {
		t.Fatalf("weather = %+v, %v", res, err)
	}
	e2eTEcho(t, c, "modern sse")
	if err := c.probe(t.Context()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if srv.count(methodDiscover) != 3 || srv.count(methodPing) != 0 {
		t.Errorf("health check: %v", srv.snapshot())
	}

	if err := e2eTCancelSleep(t, c, srv); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call error = %v, want context.Canceled", err)
	}
	e2eTEventually(t, "sleep POST aborted", func() bool { return srv.abortedCalls() == 1 })

	ctx, cancel := context.WithTimeout(t.Context(), e2eTWait)
	defer cancel()
	if err := c.close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if srv.count(methodCancelled) != 0 || srv.count(http.MethodDelete) != 0 || srv.count("reply") != 0 {
		t.Errorf("modern framing: %v", srv.snapshot())
	}
	srv.checkClean(t)
}

// TestE2E_HTTPAuthFailure: a 401 is never an era verdict. While the
// server refuses Thane's credentials no initialize is attempted, the
// era stays unknown and calls report the server unavailable; once it
// accepts them the client connects. A 401 on a ready connection reaches
// the model as an auth failure and neither the call nor the health
// check tears the connection down.
func TestE2E_HTTPAuthFailure(t *testing.T) {
	t.Parallel()
	srv := e2eTHTTPStart(t, e2eTStyleModern)
	srv.setStatus(http.StatusUnauthorized)
	c := e2eTHTTPClient(t, srv, protocolAuto, func(cfg *clientConfig) {
		cfg.backoffMin, cfg.backoffMax = 20*time.Millisecond, 50*time.Millisecond
	})
	e2eTStart(t, c)

	_, err := c.readyConn()
	var statusErr *httpStatusError
	if !errors.Is(err, errNotReady) || !errors.As(err, &statusErr) || statusErr.Status != http.StatusUnauthorized ||
		statusErr.WWWAuthenticate != `Bearer realm="e2e"` {
		t.Fatalf("readyConn error = %v, want errNotReady wrapping a 401", err)
	}
	if srv.count(methodInitialize) != 0 || c.cachedEra() != eraUnknown {
		t.Fatalf("a 401 was taken as an era verdict: initialize sent %d times, era %s", srv.count(methodInitialize), c.cachedEra())
	}
	_, err = c.callTool(t.Context(), "echo", map[string]any{"text": "x"})
	var unavailable tools.ErrUnavailable
	if !errors.As(modelFacingCallError("mcp_fake_echo", err), &unavailable) {
		t.Fatalf("call while unauthorized = %v, want the tool reported unavailable", err)
	}

	srv.setStatus(0)
	conn := e2eTReady(t, c)
	e2eTCheckConn(t, conn, eraModern, modernProtocolVersion)
	if srv.count(methodInitialize) != 0 {
		t.Errorf("initialize sent to a modern server: %v", srv.snapshot())
	}

	srv.setStatus(http.StatusUnauthorized)
	_, err = c.callTool(t.Context(), "echo", map[string]any{"text": "x"})
	if got := e2eTModelText(t, err); got != textAuth {
		t.Errorf("model text for a 401 call = %q", got)
	}
	if err := c.probe(t.Context()); !errors.As(err, &statusErr) || statusErr.Status != http.StatusUnauthorized {
		t.Errorf("probe = %v, want the 401", err)
	}
	if e2eTReady(t, c) != conn {
		t.Error("a 401 tore down the ready connection")
	}
	srv.setStatus(0)
	e2eTEcho(t, c, "authorized again")
	srv.checkClean(t)
}

// TestE2E_HTTPServerDownAtBoot: a server that is not listening when
// Thane starts is no era verdict; the supervisor keeps retrying and
// connects once the server comes up, with no restart of Thane.
func TestE2E_HTTPServerDownAtBoot(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens until the server "boots"

	srv := &e2eTHTTP{style: e2eTStyleStateless, sessions: map[string]bool{}, seen: map[string]int{}, waiting: map[string]chan *wireMessage{}}
	srv.Server = httptest.NewUnstartedServer(srv)
	t.Cleanup(srv.Close)
	srv.URL = "http://" + addr // the factory takes the URL now; Start sets the same one
	c := e2eTHTTPClient(t, srv, protocolAuto, func(cfg *clientConfig) {
		cfg.backoffMin, cfg.backoffMax = 20*time.Millisecond, 50*time.Millisecond
	})
	e2eTStart(t, c)
	if _, err := c.readyConn(); !errors.Is(err, errNotReady) || !errors.Is(err, errUnreachable) {
		t.Fatalf("readyConn while down = %v, want errNotReady wrapping errUnreachable", err)
	}
	if got := c.cachedEra(); got != eraUnknown {
		t.Fatalf("era %s decided while the server was down", got)
	}

	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("could not re-listen on %s: %v", addr, err)
	}
	srv.Listener = ln
	srv.Start()
	e2eTCheckConn(t, e2eTReady(t, c), eraLegacy, handshakeOfferVersion)
	e2eTEcho(t, c, "up now")
	srv.checkClean(t)
}
