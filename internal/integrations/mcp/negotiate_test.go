package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// clientTNegCase drives one negotiation against a scripted server.
type clientTNegCase struct {
	name      string
	transport string // default stdio
	protocol  string // default auto
	lastEra   era
	routes    func() clientTRoutes
	dialErr   func(spawn int) error
	attempt   time.Duration // negotiate ctx; default 5s

	wantEra    era // eraUnknown: expect failure
	wantVer    string
	wantErrIs  error
	wantErrHas string
	wantCalls  []string // outbound methods in order
	wantSpawns int      // default 1
}

// clientTOverride returns base() with method answered by h.
func clientTOverride(base func() clientTRoutes, overrides map[string]clientTHandler) func() clientTRoutes {
	return func() clientTRoutes {
		r := base()
		for m, h := range overrides {
			r[m] = h
		}
		return r
	}
}

func clientTLegacyBase() clientTRoutes { return clientTLegacyRoutes("") }
func clientTModernBase() clientTRoutes { return clientTModernRoutes() }

func clientTRunNeg(t *testing.T, tt clientTNegCase) {
	t.Helper()
	if tt.transport == "" {
		tt.transport = transportStdio
	}
	srv := &clientTFakeServer{handle: tt.routes().handler(), dialErr: tt.dialErr}
	c := clientTNew(t, srv, clientTConfig(tt.transport, tt.protocol))
	c.rememberEra(tt.lastEra)
	attempt := tt.attempt
	if attempt == 0 {
		attempt = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), attempt)
	defer cancel()
	conn, err := c.negotiate(ctx)
	if conn != nil {
		defer c.closeConn(conn)
	}

	if tt.wantEra == eraUnknown {
		if err == nil {
			t.Fatalf("negotiate succeeded with era %v, want failure", conn.currentEra())
		}
		if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
			t.Errorf("err = %v, want errors.Is %v", err, tt.wantErrIs)
		}
		if tt.wantErrHas != "" && !strings.Contains(err.Error(), tt.wantErrHas) {
			t.Errorf("err = %q, want it to contain %q", err, tt.wantErrHas)
		}
	} else {
		if err != nil {
			t.Fatalf("negotiate: %v", err)
		}
		if got := conn.currentEra(); got != tt.wantEra {
			t.Errorf("era = %v, want %v", got, tt.wantEra)
		}
		if conn.version != tt.wantVer {
			t.Errorf("version = %q, want %q", conn.version, tt.wantVer)
		}
		if got := c.cachedEra(); got != tt.wantEra {
			t.Errorf("cached era = %v, want %v", got, tt.wantEra)
		}
	}
	if tt.wantCalls != nil {
		if got := srv.methods(); !slices.Equal(got, tt.wantCalls) {
			t.Errorf("methods = %q, want %q", got, tt.wantCalls)
		}
	}
	wantSpawns := tt.wantSpawns
	if wantSpawns == 0 && tt.dialErr == nil {
		wantSpawns = 1
	}
	if got := srv.spawns(); got != wantSpawns {
		t.Errorf("spawns = %d, want %d", got, wantSpawns)
	}
	for _, m := range srv.messages() {
		if (m.method == methodDiscover || m.method == methodInitialize) && m.info.cancellable {
			t.Errorf("%s was sent cancellable; negotiation requests are never cancelled", m.method)
		}
	}
}

var (
	clientTLegacyFallback = []string{methodDiscover, methodInitialize, methodInitialized}
	clientTModernOnly     = []string{methodDiscover}
)

// TestNegotiate_StdioProbeOutcomes covers every stdio probe outcome:
// modern results and errors, non-modern errors, timeout, and exit or EOF.
func TestNegotiate_StdioProbeOutcomes(t *testing.T) {
	disc := func(h clientTHandler) func() clientTRoutes {
		return clientTOverride(clientTLegacyBase, map[string]clientTHandler{methodDiscover: h})
	}
	modernAfter := func(first clientTHandler) func() clientTRoutes {
		return clientTOverride(clientTModernBase, map[string]clientTHandler{
			methodDiscover: clientTSeq(first, clientTModernGuard(clientTReply(clientTOK(clientTModernResult), nil))),
		})
	}
	rpc := func(code int, data string) clientTHandler { return clientTReply(clientTRPCErr(code, data), nil) }
	cases := []clientTNegCase{
		{name: "valid discover result is modern", routes: clientTModernBase,
			wantEra: eraModern, wantVer: modernProtocolVersion, wantCalls: clientTModernOnly},
		{name: "discover result with only handshake versions initializes at the best",
			routes:  disc(clientTReply(clientTOK(`{"supportedVersions":["2025-03-26","2025-06-18"],"capabilities":{}}`), nil)),
			wantEra: eraLegacy, wantVer: "2025-06-18", wantCalls: clientTLegacyFallback},
		{name: "unparseable discover result is legacy", routes: disc(clientTReply(clientTOK(`"nope"`), nil)),
			wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: clientTLegacyFallback},
		{name: "discover result without capabilities is legacy",
			routes:  disc(clientTReply(clientTOK(`{"supportedVersions":["2026-07-28"]}`), nil)),
			wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: clientTLegacyFallback},
		{name: "-32022 naming a modern version retries discover once",
			routes:  modernAfter(rpc(codeUnsupportedProtocolVersion, `{"supported":["2026-07-28"]}`)),
			wantEra: eraModern, wantVer: modernProtocolVersion, wantCalls: []string{methodDiscover, methodDiscover}},
		{name: "-32022 naming only handshake versions initializes at the best",
			routes:  disc(rpc(codeUnsupportedProtocolVersion, `{"supported":["2024-11-05","2025-06-18"]}`)),
			wantEra: eraLegacy, wantVer: "2025-06-18", wantCalls: clientTLegacyFallback},
		{name: "-32022 with no mutual version fails naming the server's list",
			routes:    disc(rpc(codeUnsupportedProtocolVersion, `{"supported":["1999-01-01"]}`)),
			wantErrIs: errIncompatible, wantErrHas: "1999-01-01", wantCalls: clientTModernOnly},
		{name: "-32022 with unparseable data is legacy",
			routes:  disc(rpc(codeUnsupportedProtocolVersion, `"garbage"`)),
			wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: clientTLegacyFallback},
		{name: "-32022 retried discover answered -32022 again fails",
			routes:    disc(rpc(codeUnsupportedProtocolVersion, `{"supported":["2026-07-28"]}`)),
			wantErrIs: errIncompatible, wantCalls: []string{methodDiscover, methodDiscover}},
		{name: "-32021 fails: Thane declares no capabilities", routes: disc(rpc(codeMissingClientCapability, "")),
			wantErrIs: errIncompatible, wantCalls: clientTModernOnly},
		{name: "-32020 fails as a Thane framing bug", routes: disc(rpc(codeHeaderMismatch, "")),
			wantErrIs: errProtocol, wantCalls: clientTModernOnly},
		{name: "probe timeout is legacy on the same process", routes: disc(clientTHang),
			wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: clientTLegacyFallback},
		{name: "process exit during the probe respawns once for initialize",
			routes:  disc(clientTLoseWith(fmt.Errorf("%w: exit status 1", errConnectionLost))),
			wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: clientTLegacyFallback, wantSpawns: 2},
		{name: "stdout closed before the probe was written respawns once",
			routes:  disc(clientTLoseWith(fmt.Errorf("%w: write failed", errUnreachable))),
			wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: clientTLegacyFallback, wantSpawns: 2},
		{name: "respawned process dying again fails the attempt",
			routes: clientTOverride(clientTLegacyBase, map[string]clientTHandler{
				methodDiscover:   clientTLoseWith(errConnectionLost),
				methodInitialize: clientTLoseWith(errConnectionLost),
			}),
			wantErrIs: errConnectionLost, wantCalls: []string{methodDiscover, methodInitialize}, wantSpawns: 2},
		{name: "spawn failure fails the attempt", routes: clientTLegacyBase,
			dialErr:   func(int) error { return fmt.Errorf("%w: exec: not found", errUnreachable) },
			wantErrIs: errUnreachable, wantCalls: []string{}},
	}
	for _, code := range []int{codeMethodNotFound, codeInvalidParams, codeInvalidRequest, -32603, -32000, -32019, 0, 42} {
		cases = append(cases, clientTNegCase{
			name:   fmt.Sprintf("error code %d is legacy", code),
			routes: disc(rpc(code, "")), wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: clientTLegacyFallback,
		})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) { clientTRunNeg(t, tt) })
	}
}

// TestNegotiate_HTTPProbeOutcomes covers every HTTP probe outcome:
// 2xx bodies, 400/404/405 bodies, and the statuses that are no verdict.
func TestNegotiate_HTTPProbeOutcomes(t *testing.T) {
	disc := func(h clientTHandler) func() clientTRoutes {
		return clientTOverride(clientTLegacyBase, map[string]clientTHandler{methodDiscover: h})
	}
	status := func(code int, rpc *RPCError) clientTHandler { return clientTReply(nil, clientTStatus(code, rpc)) }
	modernAfter := func(first clientTHandler) func() clientTRoutes {
		return clientTOverride(clientTModernBase, map[string]clientTHandler{
			methodDiscover: clientTSeq(first, clientTModernGuard(clientTReply(clientTOK(clientTModernResult), nil))),
		})
	}
	cases := []clientTNegCase{
		{name: "network error is no verdict", routes: disc(clientTReply(nil, fmt.Errorf("%w: connection refused", errUnreachable))),
			wantErrIs: errUnreachable, wantCalls: clientTModernOnly},
		{name: "connection lost is no verdict and never respawns",
			routes:    disc(clientTReply(nil, fmt.Errorf("%w: reset", errConnectionLost))),
			wantErrIs: errConnectionLost, wantCalls: clientTModernOnly},
		{name: "timeout is no verdict", routes: disc(clientTHang),
			wantErrIs: context.DeadlineExceeded, wantCalls: clientTModernOnly},
		{name: "refused redirect is no verdict", routes: disc(clientTReply(nil, fmt.Errorf("%w: refused redirect", errProtocol))),
			wantErrIs: errProtocol, wantCalls: clientTModernOnly},
		{name: "2xx DiscoverResult is modern", routes: clientTModernBase,
			wantEra: eraModern, wantVer: modernProtocolVersion, wantCalls: clientTModernOnly},
		{name: "200 with -32601 is legacy", routes: clientTLegacyBase,
			wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: clientTLegacyFallback},
		{name: "200 with -32021 fails", routes: disc(clientTReply(clientTRPCErr(codeMissingClientCapability, ""), nil)),
			wantErrIs: errIncompatible, wantCalls: clientTModernOnly},
		{name: "200 with -32022 naming the modern version retries discover",
			routes:  modernAfter(clientTReply(clientTRPCErr(codeUnsupportedProtocolVersion, `{"supported":["2026-07-28"]}`), nil)),
			wantEra: eraModern, wantVer: modernProtocolVersion, wantCalls: []string{methodDiscover, methodDiscover}},
		{name: "400 with -32022 naming the modern version retries discover",
			routes:  modernAfter(status(400, clientTRPC(codeUnsupportedProtocolVersion, `{"supported":["2026-07-28"]}`))),
			wantEra: eraModern, wantVer: modernProtocolVersion, wantCalls: []string{methodDiscover, methodDiscover}},
		{name: "400 with -32020 fails", routes: disc(status(400, clientTRPC(codeHeaderMismatch, ""))),
			wantErrIs: errProtocol, wantCalls: clientTModernOnly},
		{name: "404 with -32601 is legacy", routes: disc(status(404, clientTRPC(codeMethodNotFound, ""))),
			wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: clientTLegacyFallback},
		{name: "400 with a session-required -32000 is legacy", routes: disc(status(400, clientTRPC(-32000, ""))),
			wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: clientTLegacyFallback},
		{name: "initialize 404 without a JSON-RPC body hints at HTTP+SSE",
			routes: clientTOverride(clientTLegacyBase, map[string]clientTHandler{
				methodDiscover:   status(404, nil),
				methodInitialize: status(404, nil),
			}),
			wantErrHas: "HTTP+SSE", wantCalls: []string{methodDiscover, methodInitialize}},
	}
	for _, code := range []int{400, 404, 405} {
		cases = append(cases, clientTNegCase{
			name:   fmt.Sprintf("%d without a JSON-RPC body is legacy", code),
			routes: disc(status(code, nil)), wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: clientTLegacyFallback,
		})
	}
	for _, code := range []int{401, 403, 406, 415, 408, 429, 500, 502, 503, 302, 418} {
		cases = append(cases, clientTNegCase{
			name:       fmt.Sprintf("status %d is no verdict", code),
			routes:     disc(status(code, nil)),
			wantErrHas: fmt.Sprintf("http status %d", code), wantCalls: clientTModernOnly,
		})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tt.transport = transportHTTP
			clientTRunNeg(t, tt)
		})
	}
}

// TestNegotiate_InitializeOutcomes covers every reply to the legacy
// initialize, including a -32022 re-probe and a one-time version re-offer.
func TestNegotiate_InitializeOutcomes(t *testing.T) {
	initWith := func(h clientTHandler) func() clientTRoutes {
		return clientTOverride(clientTLegacyBase, map[string]clientTHandler{methodInitialize: h})
	}
	rpc := func(code int, data string) clientTHandler { return clientTReply(clientTRPCErr(code, data), nil) }
	cases := []clientTNegCase{
		{name: "server counter-offers an older handshake version", routes: initWith(clientTInitialize("2025-03-26")),
			wantEra: eraLegacy, wantVer: "2025-03-26", wantCalls: clientTLegacyFallback},
		{name: "unsupported chosen version fails", routes: initWith(clientTInitialize("1.0")),
			wantErrIs: errIncompatible, wantErrHas: `"1.0"`, wantCalls: []string{methodDiscover, methodInitialize}},
		{name: "a modern version chosen in initialize fails", routes: initWith(clientTInitialize(modernProtocolVersion)),
			wantErrIs: errIncompatible, wantCalls: []string{methodDiscover, methodInitialize}},
		{name: "-32022 to initialize re-probes discover",
			routes: clientTOverride(clientTModernBase, map[string]clientTHandler{
				methodDiscover: clientTSeq(clientTHang, clientTModernGuard(clientTReply(clientTOK(clientTModernResult), nil))),
			}),
			wantEra: eraModern, wantVer: modernProtocolVersion,
			wantCalls: []string{methodDiscover, methodInitialize, methodDiscover}},
		{name: "an error naming a handshake version re-offers it once",
			routes:  initWith(clientTSeq(rpc(codeInvalidParams, `{"supported":["2024-11-05"]}`), clientTInitialize(""))),
			wantEra: eraLegacy, wantVer: "2024-11-05",
			wantCalls: []string{methodDiscover, methodInitialize, methodInitialize, methodInitialized}},
		{name: "the re-offer is bounded to once", routes: initWith(rpc(codeInvalidParams, `{"supported":["2024-11-05"]}`)),
			wantErrIs: errIncompatible, wantCalls: []string{methodDiscover, methodInitialize, methodInitialize}},
		{name: "initialize error without versions fails", routes: initWith(rpc(-32603, "")),
			wantErrHas: "initialize: rejected: jsonrpc error", wantCalls: []string{methodDiscover, methodInitialize}},
		{name: "initialize timeout fails the attempt", routes: initWith(clientTHang), attempt: 300 * time.Millisecond,
			wantErrIs: context.DeadlineExceeded, wantCalls: []string{methodDiscover, methodInitialize}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) { clientTRunNeg(t, tt) })
	}
}

// TestNegotiate_InitializedNotifyFailureFailsTheAttempt checks that a
// legacy handshake is only complete once notifications/initialized was
// accepted.
func TestNegotiate_InitializedNotifyFailureFailsTheAttempt(t *testing.T) {
	srv := &clientTFakeServer{
		handle: clientTLegacyBase().handler(),
		notify: func(*clientTFakeTransport, *Notification, callInfo) error {
			return clientTStatus(500, nil)
		},
	}
	c := clientTNew(t, srv, clientTConfig(transportHTTP, ""))
	conn, err := c.negotiate(t.Context())
	if conn != nil {
		defer c.closeConn(conn)
	}
	if err == nil || !strings.Contains(err.Error(), methodInitialized) {
		t.Fatalf("err = %v, want it to mention %s", err, methodInitialized)
	}
}

// TestNegotiate_ProtocolModes covers protocol: legacy|modern and the
// auto era cache.
func TestNegotiate_ProtocolModes(t *testing.T) {
	cases := []clientTNegCase{
		{name: "legacy mode skips the probe", protocol: protocolLegacy, routes: clientTLegacyBase,
			wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: []string{methodInitialize, methodInitialized}},
		{name: "legacy mode fails on a modern-only server without probing", protocol: protocolLegacy, routes: clientTModernBase,
			wantErrIs: errIncompatible, wantErrHas: "set protocol to auto or modern", wantCalls: []string{methodInitialize}},
		{name: "modern mode fails on a legacy server without initialize", protocol: protocolModern, routes: clientTLegacyBase,
			wantErrIs: errIncompatible, wantErrHas: "set protocol to auto or legacy", wantCalls: clientTModernOnly},
		{name: "modern mode does not respawn", protocol: protocolModern,
			routes:    clientTOverride(clientTLegacyBase, map[string]clientTHandler{methodDiscover: clientTLoseWith(errConnectionLost)}),
			wantErrIs: errConnectionLost, wantCalls: clientTModernOnly},
		{name: "modern mode waits past the probe timeout", protocol: protocolModern,
			routes: clientTOverride(clientTModernBase, map[string]clientTHandler{
				methodDiscover: clientTDelay(300*time.Millisecond, clientTReply(clientTOK(clientTModernResult), nil)),
			}),
			wantEra: eraModern, wantVer: modernProtocolVersion, wantCalls: clientTModernOnly},
		{name: "auto after a legacy era starts with initialize", lastEra: eraLegacy, routes: clientTLegacyBase,
			wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: []string{methodInitialize, methodInitialized}},
		{name: "auto after a legacy era re-probes when initialize says modern", lastEra: eraLegacy, routes: clientTModernBase,
			wantEra: eraModern, wantVer: modernProtocolVersion, wantCalls: []string{methodInitialize, methodDiscover}},
		{name: "auto after a modern era still probes", lastEra: eraModern, routes: clientTLegacyBase,
			wantEra: eraLegacy, wantVer: handshakeOfferVersion, wantCalls: clientTLegacyFallback},
		{name: "unknown protocol setting is auto", protocol: "bogus", routes: clientTModernBase,
			wantEra: eraModern, wantVer: modernProtocolVersion, wantCalls: clientTModernOnly},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) { clientTRunNeg(t, tt) })
	}
}

// TestNegotiate_CachedLegacyEra checks when a cached legacy era is
// dropped: only when the server itself rejects initialize.
func TestNegotiate_CachedLegacyEra(t *testing.T) {
	cases := []struct {
		name string
		init clientTHandler
		want era
	}{
		{name: "server rejects initialize", init: clientTReply(clientTRPCErr(-32603, ""), nil), want: eraUnknown},
		{name: "server answers 400 without a body", init: clientTReply(nil, clientTStatus(400, nil)), want: eraUnknown},
		{name: "outage keeps the cached era", init: clientTReply(nil, errUnreachable), want: eraLegacy},
		{name: "auth failure keeps the cached era", init: clientTReply(nil, clientTStatus(401, nil)), want: eraLegacy},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			routes := clientTLegacyBase()
			routes[methodInitialize] = tt.init
			srv := &clientTFakeServer{handle: routes.handler()}
			c := clientTNew(t, srv, clientTConfig(transportHTTP, ""))
			c.rememberEra(eraLegacy)
			conn, err := c.negotiate(t.Context())
			if conn != nil {
				defer c.closeConn(conn)
			}
			if err == nil {
				t.Fatal("negotiate succeeded, want failure")
			}
			if got := c.cachedEra(); got != tt.want {
				t.Errorf("cached era = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNegotiate_RespawnClosesTheDeadTransport: the process that died
// during the probe is closed, negotiation continues on the respawned
// one, and the respawn is a Debug detail (it happens on every connect
// attempt to a legacy server that exits on unknown requests).
func TestNegotiate_RespawnClosesTheDeadTransport(t *testing.T) {
	routes := clientTLegacyBase()
	routes[methodDiscover] = clientTLoseWith(errConnectionLost)
	srv := &clientTFakeServer{handle: routes.handler()}
	cfg := clientTConfig(transportStdio, "")
	logs := &coreTLogBuffer{}
	cfg.logger = logs.logger()
	c := clientTNew(t, srv, cfg)
	conn, err := c.negotiate(t.Context())
	if err != nil {
		t.Fatalf("negotiate: %v", err)
	}
	defer c.closeConn(conn)
	first, second := srv.transportAt(0), srv.transportAt(1)
	if !first.isClosed() {
		t.Error("the transport that died during the probe was not closed")
	}
	if conn.tr != second || second.isClosed() {
		t.Error("negotiation did not continue on the respawned transport")
	}
	const respawn = `msg="mcp: server exited during the era probe; restarting it for the legacy handshake"`
	if out := logs.String(); !strings.Contains(out, "level=DEBUG "+respawn) || strings.Contains(out, "level=INFO "+respawn) {
		t.Errorf("respawn should log at Debug only:\n%s", out)
	}
}

// TestNegotiate_StdioProbeLeavesRoomForInitialize: when the attempt
// budget is no larger than the probe timeout, a silent stdio legacy
// server is still detected. The probe takes at most half the attempt,
// so its timeout leaves time for initialize and tools/list.
func TestNegotiate_StdioProbeLeavesRoomForInitialize(t *testing.T) {
	routes := clientTLegacyRoutes("", clientTTool("echo"))
	routes[methodDiscover] = clientTHang // never answers unknown requests
	srv := &clientTFakeServer{handle: routes.handler()}
	cfg := clientTConfig(transportStdio, protocolAuto)
	cfg.probeTimeout, cfg.connectTimeout = time.Second, time.Second
	c := clientTNew(t, srv, cfg)
	clientTStart(t, c)
	clientTReadyConn(t, c)
	want := []string{methodDiscover, methodInitialize, methodInitialized, methodToolsList}
	if got := srv.methods(); !slices.Equal(got, want) {
		t.Errorf("methods = %q, want %q (one attempt: probe timeout, then legacy)", got, want)
	}
}

// TestNegotiate_LegacyFraming checks legacy framing on the wire: no modern
// _meta anywhere, initialize without the version header,
// notifications/initialized before any other request, and the version
// header afterwards only from 2025-06-18 on.
func TestNegotiate_LegacyFraming(t *testing.T) {
	for _, tt := range []struct {
		version    string
		wantHeader string
	}{
		{version: "2025-11-25", wantHeader: "2025-11-25"},
		{version: "2025-06-18", wantHeader: "2025-06-18"},
		{version: "2025-03-26", wantHeader: ""},
		{version: "2024-11-05", wantHeader: ""},
	} {
		t.Run(tt.version, func(t *testing.T) {
			srv := &clientTFakeServer{handle: clientTLegacyRoutes(tt.version, clientTTool("echo")).handler()}
			c := clientTNew(t, srv, clientTConfig(transportHTTP, ""))
			clientTStart(t, c)
			clientTReadyConn(t, c)
			if _, err := c.callTool(t.Context(), "echo", nil); err != nil {
				t.Fatalf("callTool: %v", err)
			}
			if err := c.probe(t.Context()); err != nil {
				t.Fatalf("probe: %v", err)
			}
			msgs := srv.messages()
			want := []string{methodDiscover, methodInitialize, methodInitialized, methodToolsList, methodToolsCall, methodPing}
			if got := srv.methods(); !slices.Equal(got, want) {
				t.Fatalf("methods = %q, want %q", got, want)
			}
			for i, m := range msgs[1:] {
				if strings.Contains(string(m.params), "io.modelcontextprotocol/") {
					t.Errorf("%s carries modern _meta: %s", m.method, m.params)
				}
				if m.info.modern {
					t.Errorf("%s framed modern", m.method)
				}
				wantHeader := tt.wantHeader
				if i == 0 {
					wantHeader = "" // initialize never carries the version header
				}
				if m.info.protocolVersion != wantHeader {
					t.Errorf("%s protocolVersion = %q, want %q", m.method, m.info.protocolVersion, wantHeader)
				}
			}
			if msgs[2].call {
				t.Error("notifications/initialized was sent as a request")
			}
			var initParams struct {
				ProtocolVersion string         `json:"protocolVersion"`
				Capabilities    map[string]any `json:"capabilities"`
				ClientInfo      implementation `json:"clientInfo"`
			}
			if err := json.Unmarshal(msgs[1].params, &initParams); err != nil {
				t.Fatal(err)
			}
			if initParams.ProtocolVersion != handshakeOfferVersion || initParams.Capabilities == nil ||
				initParams.ClientInfo.Name != clientInfoName {
				t.Errorf("initialize params = %s", msgs[1].params)
			}
			if msgs[3].params != nil {
				t.Errorf("first legacy tools/list params = %s, want omitted", msgs[3].params)
			}
		})
	}
}

// TestNegotiate_ModernFraming checks that every modern request carries
// the per-request _meta and modern framing, and tools/call its Mcp-Name.
func TestNegotiate_ModernFraming(t *testing.T) {
	srv := &clientTFakeServer{handle: clientTModernRoutes(clientTTool("echo")).handler()}
	c := clientTNew(t, srv, clientTConfig(transportHTTP, ""))
	clientTStart(t, c)
	clientTReadyConn(t, c)
	res, err := c.callTool(t.Context(), "echo", map[string]any{"text": "hi"})
	if err != nil || !strings.Contains(res.Text, `{"text":"hi"}`) {
		t.Fatalf("callTool = %+v, %v", res, err)
	}
	if err := c.probe(t.Context()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	want := []string{methodDiscover, methodToolsList, methodToolsCall, methodDiscover}
	if got := srv.methods(); !slices.Equal(got, want) {
		t.Fatalf("methods = %q, want %q (a modern server is never pinged)", got, want)
	}
	for _, m := range srv.messages() {
		if !clientTHasModernMeta(m.params) {
			t.Errorf("%s params lack the modern _meta: %s", m.method, m.params)
		}
		if !m.info.modern || m.info.protocolVersion != modernProtocolVersion {
			t.Errorf("%s info = %+v, want modern framing", m.method, m.info)
		}
		if m.method == methodToolsCall {
			if m.info.name != "echo" || !m.info.cancellable {
				t.Errorf("tools/call info = %+v, want Mcp-Name echo, cancellable", m.info)
			}
		} else if m.info.name != "" {
			t.Errorf("%s carries Mcp-Name %q", m.method, m.info.name)
		}
	}
}

// TestNegotiate_StoresServerDetails checks what negotiation keeps.
func TestNegotiate_StoresServerDetails(t *testing.T) {
	for _, tt := range []struct {
		name, server, instructions string
		routes                     clientTRoutes
	}{
		{name: "legacy", server: "legacy-fake", instructions: "legacy instructions", routes: clientTLegacyRoutes("")},
		{name: "modern", server: "modern-fake", instructions: "modern instructions", routes: clientTModernRoutes()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := &clientTFakeServer{handle: tt.routes.handler()}
			c := clientTNew(t, srv, clientTConfig(transportStdio, ""))
			conn, err := c.negotiate(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer c.closeConn(conn)
			if conn.server.Name != tt.server || conn.instructions != tt.instructions {
				t.Errorf("server = %+v, instructions = %q", conn.server, conn.instructions)
			}
		})
	}
}
