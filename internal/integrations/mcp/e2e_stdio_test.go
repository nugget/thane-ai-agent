package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Helper-process modes for the end-to-end tests (see stdioTHelper). All
// of them serve the mcp-legacy / mcp-modern tools plus the e2e tools
// (e2eTool) and answer e2eTMethodStats.
const (
	e2eTModePrefix = "e2e-"
	e2eTModeLegacy = "e2e-legacy" // legacy-only, like mcp-legacy
	e2eTModeModern = "e2e-modern" // modern-only, like mcp-modern
	// e2eTModeFragile is legacy-only and exits on any request it does
	// not know before initialize, as some early servers did (A17).
	e2eTModeFragile = "e2e-fragile"
	// e2eTModeDual is dual-era with an era lock, like the Python SDK's
	// serve_dual_era_loop: the first request decides the era of the
	// connection (modern if it is not initialize and carries the modern
	// _meta envelope). A legacy-locked connection rejects enveloped
	// requests with -32600; a modern-locked one answers initialize with
	// -32022 naming the modern versions.
	e2eTModeDual = "e2e-dual"

	// e2eTDelayEnv delays the helper's first read of stdin by this many
	// milliseconds (a slow-starting server: npx, docker).
	e2eTDelayEnv = "MCP_E2E_START_DELAY_MS"

	// e2eTMethodStats returns e2eTStats; it never decides the era.
	e2eTMethodStats = "e2e/stats"

	e2eTCrashName = "crash"
)

// e2eTExtraTools are the e2e tools listed on the second tools/list page:
// "crash" exits the process mid-call; "grow" adds the tool "extra" and
// sends notifications/tools/list_changed; "ask" sends the client a ping
// whose id collides with the client's own first id, and a roots/list,
// and reports the replies.
var e2eTExtraTools = []string{
	`{"name":"crash","description":"Exit the server process mid-call.","inputSchema":{"type":"object"}}`,
	`{"name":"grow","description":"Add the tool extra.","inputSchema":{"type":"object"}}`,
	`{"name":"ask","description":"Ask the client two questions.","inputSchema":{"type":"object"}}`,
}

const e2eTGrownTool = `{"name":"extra","description":"Added by grow.","inputSchema":{"type":"object"}}`

// e2eTServerState is the e2e modes' part of stdioTServer, guarded by
// stdioTServer.mu.
type e2eTServerState struct {
	era   era            // locked era; eraUnknown before the first request
	seen  map[string]int // requests per method, stats included
	grown bool           // "grow" ran: "extra" is listed
}

// e2eTStats is what e2eTMethodStats reports about one server process.
type e2eTStats struct {
	PID       int            `json:"pid"`
	Era       string         `json:"era"`
	Seen      map[string]int `json:"seen"`
	Cancelled []stdioTCancel `json:"cancelled"`
}

// e2eTStartDelay sleeps for the e2eTDelayEnv milliseconds, if set.
func e2eTStartDelay() {
	if ms, err := strconv.Atoi(os.Getenv(e2eTDelayEnv)); err == nil && ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
}

// e2eTCrash ends the helper process as a crashing server would.
func e2eTCrash() {
	fmt.Fprintln(os.Stderr, "e2e: crashing as asked")
	os.Exit(3)
}

// answerE2E runs on the helper's stdin reader so that the era lock
// follows the order requests arrive in, then answers like mcp-legacy
// or mcp-modern.
func (s *stdioTServer) answerE2E(req *Request) {
	s.mu.Lock()
	if s.e2e.seen == nil {
		s.e2e.seen = map[string]int{}
	}
	s.e2e.seen[req.Method]++
	if req.Method == e2eTMethodStats {
		stats := e2eTStats{
			PID:       os.Getpid(),
			Era:       s.e2e.era.String(),
			Seen:      maps.Clone(s.e2e.seen),
			Cancelled: slices.Clone(s.cancelled),
		}
		s.mu.Unlock()
		s.result(req.ID, stats)
		return
	}
	if s.e2e.era == eraUnknown && s.mode == e2eTModeFragile && req.Method != methodInitialize {
		s.mu.Unlock()
		e2eTCrash()
	}
	if s.e2e.era == eraUnknown {
		switch {
		case s.mode == e2eTModeLegacy, s.mode == e2eTModeFragile:
			s.e2e.era = eraLegacy
		case s.mode == e2eTModeModern:
			s.e2e.era = eraModern
		case req.Method != methodInitialize && e2eTHasEnvelope(req.Params):
			s.e2e.era = eraModern
		default:
			s.e2e.era = eraLegacy
		}
	}
	modern := s.e2e.era == eraModern
	s.mu.Unlock()
	go s.answerMCP(req, modern)
}

// e2eTools returns the e2e tools for the second tools/list page.
func (s *stdioTServer) e2eTools() []json.RawMessage {
	out := make([]json.RawMessage, 0, len(e2eTExtraTools)+1)
	for _, raw := range e2eTExtraTools {
		out = append(out, json.RawMessage(raw))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.e2e.grown {
		out = append(out, json.RawMessage(e2eTGrownTool))
	}
	return out
}

// e2eTool runs an e2e tool and returns its text; false for a name that
// is not one (or outside the e2e modes).
func (s *stdioTServer) e2eTool(name string) (string, bool) {
	if !strings.HasPrefix(s.mode, e2eTModePrefix) {
		return "", false
	}
	switch name {
	case e2eTCrashName:
		e2eTCrash()
	case "grow":
		s.mu.Lock()
		s.e2e.grown = true
		s.mu.Unlock()
		s.send(&Notification{JSONRPC: jsonrpcVersion, Method: methodToolsListChanged})
		return "grown", true
	case "ask":
		return fmt.Sprintf("ping=%s roots=%s", s.e2eAsk(`1`, methodPing), s.e2eAsk(`"roots-1"`, methodRootsList)), true
	case "extra":
		s.mu.Lock()
		defer s.mu.Unlock()
		return "extra", s.e2e.grown
	}
	return "", false
}

// e2eAsk sends the client a request with the raw id and waits for its
// reply: the compact result, the error code, or "none".
func (s *stdioTServer) e2eAsk(id, method string) string {
	ch := make(chan json.RawMessage, 1)
	s.mu.Lock()
	s.waiting[id] = ch
	s.mu.Unlock()
	s.send(&Request{JSONRPC: jsonrpcVersion, ID: json.RawMessage(id), Method: method})
	select {
	case raw := <-ch:
		var resp Response
		if json.Unmarshal(raw, &resp) != nil {
			return "malformed"
		}
		if resp.Error != nil {
			return strconv.Itoa(resp.Error.Code)
		}
		return string(resp.Result)
	case <-time.After(5 * time.Second):
		return "none"
	}
}

// e2eTHasEnvelope reports whether params carry the modern _meta
// protocolVersion key.
func e2eTHasEnvelope(params json.RawMessage) bool {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	_, ok := p.Meta[metaProtocolVersion]
	return ok
}

// e2eTSpawner counts the stdio transports a factory started.
type e2eTSpawner struct {
	cfg    stdioConfig
	spawns atomic.Int32
}

func (s *e2eTSpawner) factory() transportFactory {
	return func(ctx context.Context, hooks transportHooks) (Transport, error) {
		t, err := newStdioTransport(ctx, s.cfg, hooks)
		if err != nil {
			return nil, err
		}
		s.spawns.Add(1)
		return t, nil
	}
}

// e2eTStdio builds (not starts) a client for helper mode, with an
// optional start-up delay in milliseconds.
func e2eTStdio(t *testing.T, mode, protocol string, delayMS int, tweak func(*clientConfig)) (*serverClient, *e2eTSpawner) {
	t.Helper()
	sp := &e2eTSpawner{cfg: stdioTHelper(t, mode)}
	if delayMS > 0 {
		sp.cfg.env = append(sp.cfg.env, e2eTDelayEnv+"="+strconv.Itoa(delayMS))
	}
	cfg := e2eTConfig(transportStdio, protocol)
	if tweak != nil {
		tweak(&cfg)
	}
	return e2eTNew(t, cfg, sp.factory()), sp
}

// e2eTStatsOf asks the current server process for its stats.
func e2eTStatsOf(t *testing.T, c *serverClient) e2eTStats {
	t.Helper()
	conn := e2eTReady(t, c)
	resp, err := conn.call(t.Context(), e2eTMethodStats, nil, conn.frame(false))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	var st e2eTStats
	if resp.Error != nil || json.Unmarshal(resp.Result, &st) != nil {
		t.Fatalf("stats: bad response %+v", resp)
	}
	return st
}

// TestE2E_StdioLegacyServer: auto probes with server/discover, gets
// -32601 and initializes on the same process; legacy initializes
// directly. Tools list in two pages, ping is the health check, and no
// legacy request carries modern _meta (the fake rejects it).
func TestE2E_StdioLegacyServer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		protocol  string
		discovers int
	}{
		{protocolAuto, 1},
		{protocolLegacy, 0},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			t.Parallel()
			c, sp := e2eTStdio(t, e2eTModeLegacy, tc.protocol, 0, nil)
			e2eTStart(t, c)
			conn := e2eTReady(t, c)
			e2eTCheckConn(t, conn, eraLegacy, handshakeOfferVersion)
			if conn.server.Name != "thane-test-legacy" {
				t.Errorf("server = %+v", conn.server)
			}
			e2eTCheckTools(t, c, "echo", "sleep", "crash", "grow", "ask")
			if err := c.probe(t.Context()); err != nil {
				t.Fatalf("probe: %v", err)
			}
			e2eTEcho(t, c, "hello legacy")

			st := e2eTStatsOf(t, c)
			if st.Era != "legacy" || st.Seen[methodDiscover] != tc.discovers ||
				st.Seen[methodInitialize] != 1 || st.Seen[methodPing] != 1 || st.Seen[methodToolsList] != 2 {
				t.Errorf("server stats = %+v", st)
			}
			if n := sp.spawns.Load(); n != 1 {
				t.Errorf("spawned %d processes, want 1", n)
			}
			if got := c.cachedEra(); got != eraLegacy {
				t.Errorf("cached era = %s, want legacy", got)
			}
		})
	}
}

// TestE2E_StdioModernServer: auto and modern settle on 2026-07-28 with
// one server/discover; every request carries the modern _meta (the fake
// rejects one without it), and the health check is server/discover.
func TestE2E_StdioModernServer(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{protocolAuto, protocolModern} {
		t.Run(protocol, func(t *testing.T) {
			t.Parallel()
			c, sp := e2eTStdio(t, e2eTModeModern, protocol, 0, nil)
			e2eTStart(t, c)
			conn := e2eTReady(t, c)
			e2eTCheckConn(t, conn, eraModern, modernProtocolVersion)
			if conn.server.Name != "thane-test-modern" {
				t.Errorf("server = %+v", conn.server)
			}
			e2eTCheckTools(t, c, "echo", "sleep", "crash", "grow", "ask")
			if err := c.probe(t.Context()); err != nil {
				t.Fatalf("probe: %v", err)
			}
			e2eTEcho(t, c, "hello modern")

			st := e2eTStatsOf(t, c)
			if st.Era != "modern" || st.Seen[methodDiscover] != 2 || st.Seen[methodInitialize] != 0 ||
				st.Seen[methodPing] != 0 || st.Seen[methodToolsList] != 2 {
				t.Errorf("server stats = %+v", st)
			}
			if n := sp.spawns.Load(); n != 1 {
				t.Errorf("spawned %d processes, want 1", n)
			}
		})
	}
}

// TestE2E_StdioProtocolMismatch: a forced era the server does not speak
// fails with an actionable errIncompatible, and calls report the server
// as unavailable.
func TestE2E_StdioProtocolMismatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode, protocol, hint string
	}{
		{e2eTModeModern, protocolLegacy, "set protocol to auto or modern"},
		{e2eTModeLegacy, protocolModern, "set protocol to auto or legacy"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			t.Parallel()
			c, _ := e2eTStdio(t, tc.mode, tc.protocol, 0, nil)
			e2eTStart(t, c)
			_, err := c.readyConn()
			if !errors.Is(err, errNotReady) || !errors.Is(err, errIncompatible) || !strings.Contains(err.Error(), tc.hint) {
				t.Fatalf("readyConn error = %v, want errNotReady+errIncompatible naming %q", err, tc.hint)
			}
			_, err = c.callTool(t.Context(), "echo", map[string]any{"text": "x"})
			if !errors.Is(err, errNotReady) {
				t.Fatalf("callTool error = %v, want errNotReady", err)
			}
			if err := c.probe(t.Context()); !errors.Is(err, errNotReady) {
				t.Fatalf("probe error = %v, want errNotReady", err)
			}
		})
	}
}

// TestE2E_StdioDualEraLock: against a dual-era server whose first
// request locks the connection's era, auto opens with server/discover
// and stays modern; legacy opens with initialize and never sends the
// modern envelope, which the locked-legacy server would reject.
func TestE2E_StdioDualEraLock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		protocol string
		era      era
		version  string
	}{
		{protocolAuto, eraModern, modernProtocolVersion},
		{protocolLegacy, eraLegacy, handshakeOfferVersion},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			t.Parallel()
			c, _ := e2eTStdio(t, e2eTModeDual, tc.protocol, 0, nil)
			e2eTStart(t, c)
			e2eTCheckConn(t, e2eTReady(t, c), tc.era, tc.version)
			e2eTCheckTools(t, c, "echo", "sleep", "crash", "grow", "ask")
			if err := c.probe(t.Context()); err != nil {
				t.Fatalf("probe: %v", err)
			}
			e2eTEcho(t, c, "hello dual")
			if st := e2eTStatsOf(t, c); st.Era != tc.era.String() {
				t.Errorf("server locked %s, want %s (stats %+v)", st.Era, tc.era, st)
			}
		})
	}
}

// TestE2E_StdioSlowStart: the discover probe times out while the server
// is still starting. Thane falls back to initialize on the same process
// (never cancelling the probe). A legacy server answers it; a dual-era
// or modern-only server has meanwhile locked to modern from the late
// discover and answers -32022, so Thane re-probes (A3) and goes modern.
// The late discover reply is absorbed without harm.
func TestE2E_StdioSlowStart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode               string
		era                era
		version            string
		discovers, initial int
	}{
		{e2eTModeLegacy, eraLegacy, handshakeOfferVersion, 1, 1},
		{e2eTModeDual, eraModern, modernProtocolVersion, 2, 1},
		{e2eTModeModern, eraModern, modernProtocolVersion, 2, 1},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			c, sp := e2eTStdio(t, tc.mode, protocolAuto, 1500, func(cfg *clientConfig) {
				cfg.probeTimeout = 300 * time.Millisecond
			})
			e2eTStart(t, c)
			e2eTCheckConn(t, e2eTReady(t, c), tc.era, tc.version)
			e2eTEcho(t, c, "hello slow")
			st := e2eTStatsOf(t, c)
			if st.Era != tc.era.String() || st.Seen[methodDiscover] != tc.discovers || st.Seen[methodInitialize] != tc.initial {
				t.Errorf("server stats = %+v", st)
			}
			if len(st.Cancelled) != 0 {
				t.Errorf("negotiation was cancelled: %+v", st.Cancelled)
			}
			if n := sp.spawns.Load(); n != 1 {
				t.Errorf("spawned %d processes, want 1 (a probe timeout keeps the process)", n)
			}
			if got := c.cachedEra(); got != tc.era {
				t.Errorf("cached era = %s, want %s", got, tc.era)
			}
		})
	}
}

// TestE2E_StdioCrashReconnects: the server process exits mid-call. The
// call fails as "outcome unknown", the health check reports the server
// down, the supervisor respawns it and negotiates again (legacy goes
// straight to initialize from the cached era), and calls work again.
func TestE2E_StdioCrashReconnects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode string
		era  era
		// Requests the respawned process saw: negotiation plus one
		// successful health check (ping or server/discover).
		discovers, initial, pings int
	}{
		{e2eTModeLegacy, eraLegacy, 0, 1, 1},
		{e2eTModeModern, eraModern, 2, 0, 0},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			var changes atomic.Int32
			c, sp := e2eTStdio(t, tc.mode, protocolAuto, 0, func(cfg *clientConfig) {
				cfg.backoffMin = 300 * time.Millisecond
				cfg.backoffMax = time.Second
				cfg.onToolsChanged = func() { changes.Add(1) }
			})
			e2eTStart(t, c)
			first := e2eTReady(t, c)
			before := e2eTStatsOf(t, c)

			_, err := c.callTool(t.Context(), e2eTCrashName, nil)
			if !errors.Is(err, errConnectionLost) {
				t.Fatalf("crash call error = %v, want errConnectionLost", err)
			}
			if got := e2eTModelText(t, err); got != textConnLost {
				t.Errorf("model text = %q", got)
			}
			if err := c.probe(t.Context()); err == nil {
				t.Error("probe reported a crashed server healthy")
			} else if !errors.Is(err, errNotReady) && !errors.Is(err, errUnreachable) {
				t.Errorf("probe error = %v, want errNotReady or errUnreachable", err)
			}
			if got := c.tools(); len(got) != 5 {
				t.Errorf("tools while down = %d, want the last list kept", len(got))
			}

			e2eTEventually(t, "probe healthy after the respawn", func() bool { return c.probe(t.Context()) == nil })
			if e2eTReady(t, c) == first {
				t.Fatal("still on the crashed connection")
			}
			e2eTEcho(t, c, "back again")
			after := e2eTStatsOf(t, c)
			if after.PID == before.PID {
				t.Errorf("same server pid %d after the crash", after.PID)
			}
			if after.Era != tc.era.String() || after.Seen[methodDiscover] != tc.discovers ||
				after.Seen[methodInitialize] != tc.initial || after.Seen[methodPing] != tc.pings {
				t.Errorf("respawned server stats = %+v", after)
			}
			if n := sp.spawns.Load(); n != 2 {
				t.Errorf("spawned %d processes, want 2", n)
			}
			if n := changes.Load(); n != 1 {
				t.Errorf("onToolsChanged fired %d times, want 1 (the same list came back)", n)
			}
			e2eTEventually(t, "crashed process reaped", func() bool { return !stdioTAlive(before.PID) })
		})
	}
}

// TestE2E_StdioCancellationKeepsProcess: a long call does not block
// other calls, and ending its ctx (cancel or deadline) sends
// notifications/cancelled with the matching reason, never kills the
// process, and leaves the connection usable. Closing the client stops
// the process.
func TestE2E_StdioCancellationKeepsProcess(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{e2eTModeLegacy, e2eTModeModern} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			c, sp := e2eTStdio(t, mode, protocolAuto, 0, nil)
			e2eTStart(t, c)
			conn := e2eTReady(t, c)
			pid := e2eTStatsOf(t, c).PID

			ctx, cancel := context.WithCancel(t.Context())
			var wg sync.WaitGroup
			var sleepErr error
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, sleepErr = c.callTool(ctx, "sleep", map[string]any{"ms": 60_000})
			}()
			e2eTEcho(t, c, "not blocked by the long call")
			cancel()
			wg.Wait()
			if !errors.Is(sleepErr, context.Canceled) {
				t.Fatalf("cancelled call error = %v, want context.Canceled", sleepErr)
			}
			if got := e2eTModelText(t, sleepErr); got != textCanceled {
				t.Errorf("model text = %q", got)
			}

			dctx, dcancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			_, err := c.callTool(dctx, "sleep", map[string]any{"ms": 60_000})
			dcancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timed-out call error = %v, want context.DeadlineExceeded", err)
			}

			var st e2eTStats
			e2eTEventually(t, "both cancellations delivered", func() bool {
				st = e2eTStatsOf(t, c)
				return len(st.Cancelled) == 2
			})
			if st.Cancelled[0].Reason != "cancelled" || st.Cancelled[1].Reason != "timeout" {
				t.Errorf("cancellations = %+v", st.Cancelled)
			}
			if st.PID != pid || sp.spawns.Load() != 1 {
				t.Errorf("server restarted: pid %d -> %d, %d spawns", pid, st.PID, sp.spawns.Load())
			}
			if e2eTReady(t, c) != conn {
				t.Error("connection replaced after a cancelled call")
			}
			e2eTEcho(t, c, "still serving")

			cctx, ccancel := context.WithTimeout(t.Context(), e2eTWait)
			defer ccancel()
			if err := c.close(cctx); err != nil {
				t.Fatalf("close: %v", err)
			}
			e2eTEventually(t, "server process stopped by close", func() bool { return !stdioTAlive(pid) })
		})
	}
}

// TestE2E_StdioFragileLegacyRespawn: a legacy server that exits on the
// unknown server/discover is respawned once and initialized straight
// away (A17); the era is cached, so a later reconnect never probes.
func TestE2E_StdioFragileLegacyRespawn(t *testing.T) {
	t.Parallel()
	c, sp := e2eTStdio(t, e2eTModeFragile, protocolAuto, 0, nil)
	e2eTStart(t, c)
	e2eTCheckConn(t, e2eTReady(t, c), eraLegacy, handshakeOfferVersion)
	e2eTEcho(t, c, "second process")
	if n := sp.spawns.Load(); n != 2 {
		t.Errorf("spawned %d processes, want 2 (one respawn)", n)
	}
	st := e2eTStatsOf(t, c)
	if st.Seen[methodDiscover] != 0 || st.Seen[methodInitialize] != 1 {
		t.Errorf("respawned server stats = %+v", st)
	}
	if got := c.cachedEra(); got != eraLegacy {
		t.Errorf("cached era = %s, want legacy", got)
	}

	if _, err := c.callTool(t.Context(), e2eTCrashName, nil); !errors.Is(err, errConnectionLost) {
		t.Fatalf("crash call error = %v, want errConnectionLost", err)
	}
	e2eTEventually(t, "reconnected", func() bool { return c.probe(t.Context()) == nil })
	if n := sp.spawns.Load(); n != 3 {
		t.Errorf("spawned %d processes, want 3 (the reconnect skipped the probe)", n)
	}
	e2eTEcho(t, c, "third process")
}

// TestE2E_StdioServerMessages: over real pipes, a legacy server's
// requests are answered (ping with an id that collides with Thane's own
// request ids, roots/list declined with -32601), and its
// notifications/tools/list_changed refreshes the tool list and fires
// onToolsChanged.
func TestE2E_StdioServerMessages(t *testing.T) {
	t.Parallel()
	var changes atomic.Int32
	c, _ := e2eTStdio(t, e2eTModeLegacy, protocolLegacy, 0, func(cfg *clientConfig) {
		cfg.onToolsChanged = func() { changes.Add(1) }
	})
	e2eTStart(t, c)
	e2eTReady(t, c)
	e2eTEventually(t, "first tool list published", func() bool { return changes.Load() == 1 })

	res, err := c.callTool(t.Context(), "ask", nil)
	if err != nil || res.Text != "ping={} roots=-32601" {
		t.Fatalf("ask = %+v, %v", res, err)
	}

	res, err = c.callTool(t.Context(), "grow", nil)
	if err != nil || res.Text != "grown" {
		t.Fatalf("grow = %+v, %v", res, err)
	}
	e2eTEventually(t, "list_changed refresh", func() bool { return changes.Load() == 2 })
	e2eTCheckTools(t, c, "echo", "sleep", "crash", "grow", "ask", "extra")
	if res, err := c.callTool(t.Context(), "extra", nil); err != nil || res.Text != "extra" {
		t.Fatalf("extra = %+v, %v", res, err)
	}
}

// TestE2E_StdioCloseDuringConnect: closing the client while a slow
// server is still starting ends the attempt at once and stops the
// process; nothing is left running.
func TestE2E_StdioCloseDuringConnect(t *testing.T) {
	t.Parallel()
	c, sp := e2eTStdio(t, e2eTModeLegacy, protocolAuto, 5000, nil)
	c.start()
	e2eTEventually(t, "server spawned", func() bool { return sp.spawns.Load() == 1 })

	ctx, cancel := context.WithTimeout(t.Context(), e2eTWait)
	defer cancel()
	began := time.Now()
	if err := c.close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if took := time.Since(began); took > 4*time.Second {
		t.Errorf("close took %v; it waited for the slow server", took)
	}
	select {
	case <-c.firstAttempt():
	default:
		t.Error("first attempt still open after close")
	}
	if _, err := c.readyConn(); !errors.Is(err, errNotReady) {
		t.Errorf("readyConn after close = %v, want errNotReady", err)
	}
	if n := sp.spawns.Load(); n != 1 {
		t.Errorf("spawned %d processes, want 1", n)
	}
}
