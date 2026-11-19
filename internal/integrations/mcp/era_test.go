package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// coreTResult builds a result response.
func coreTResult(result string) *Response {
	return &Response{JSONRPC: jsonrpcVersion, ID: intID(1), Result: json.RawMessage(result)}
}

// coreTRPC builds a JSON-RPC error with optional data ("" = none).
func coreTRPC(code int, data string) *RPCError {
	e := &RPCError{Code: code, Message: "error message"}
	if data != "" {
		e.Data = json.RawMessage(data)
	}
	return e
}

// coreTErrResp builds an error response.
func coreTErrResp(code int, data string) *Response {
	return &Response{JSONRPC: jsonrpcVersion, ID: intID(1), Error: coreTRPC(code, data)}
}

// coreTStatus builds an HTTP status error, optionally carrying a
// JSON-RPC error body.
func coreTStatus(status int, rpc *RPCError) error {
	return &httpStatusError{Status: status, RPC: rpc, Body: "body"}
}

type coreTVerdictCase struct {
	name      string
	transport string
	resp      *Response
	err       error
	step      verdictStep
	version   string
	errIs     error  // verdictFail: errors.Is target (nil = only require err != nil)
	errHas    string // verdictFail: substring of err text
	thaneBug  bool
}

func coreTCheckVerdict(t *testing.T, tt coreTVerdictCase, got eraVerdict) {
	t.Helper()
	if got.step != tt.step {
		t.Fatalf("step = %v (err %v), want %v", got.step, got.err, tt.step)
	}
	if got.version != tt.version {
		t.Errorf("version = %q, want %q", got.version, tt.version)
	}
	if got.thaneBug != tt.thaneBug {
		t.Errorf("thaneBug = %v, want %v", got.thaneBug, tt.thaneBug)
	}
	if tt.step != verdictFail {
		if got.err != nil {
			t.Errorf("err = %v, want nil", got.err)
		}
		return
	}
	if got.err == nil {
		t.Fatal("verdictFail without err")
	}
	if tt.errIs != nil && !errors.Is(got.err, tt.errIs) {
		t.Errorf("err = %v, want errors.Is %v", got.err, tt.errIs)
	}
	if tt.errHas != "" && !strings.Contains(got.err.Error(), tt.errHas) {
		t.Errorf("err = %q, want it to contain %q", got.err, tt.errHas)
	}
}

func TestProtocol_ClassifyDiscover(t *testing.T) {
	const (
		modernResult    = `{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{"listChanged":true}},"ttlMs":0}`
		connLost        = "connection lost"
		supportedModern = `{"supported":["2026-07-28"],"requested":"2026-07-28"}`
	)
	stdioAndHTTP := []string{transportStdio, transportHTTP}

	tests := []coreTVerdictCase{
		// A valid DiscoverResult with a mutual modern version is modern.
		{name: "valid modern result", resp: coreTResult(modernResult), step: verdictModern, version: modernProtocolVersion},
		{name: "modern among handshake versions", resp: coreTResult(`{"supportedVersions":["2025-11-25","2026-07-28"],"capabilities":{}}`), step: verdictModern, version: modernProtocolVersion},
		// Legacy advertisements and unparseable results mean legacy.
		{name: "only handshake versions", resp: coreTResult(`{"supportedVersions":["2025-11-25","2025-06-18"],"capabilities":{}}`), step: verdictInitialize, version: "2025-11-25"},
		{name: "older handshake version only", resp: coreTResult(`{"supportedVersions":["2025-06-18"],"capabilities":{}}`), step: verdictInitialize, version: "2025-06-18"},
		{name: "no known version", resp: coreTResult(`{"supportedVersions":["1999-01-01"],"capabilities":{}}`), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "missing supportedVersions", resp: coreTResult(`{"capabilities":{}}`), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "missing capabilities", resp: coreTResult(`{"supportedVersions":["2026-07-28"]}`), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "null capabilities", resp: coreTResult(`{"supportedVersions":["2026-07-28"],"capabilities":null}`), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "supportedVersions not an array", resp: coreTResult(`{"supportedVersions":"2026-07-28","capabilities":{}}`), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "capabilities not an object", resp: coreTResult(`{"supportedVersions":["2026-07-28"],"capabilities":[]}`), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "null result", resp: coreTResult(`null`), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "empty result object", resp: coreTResult(`{}`), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "array result", resp: coreTResult(`[]`), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "response with neither", resp: &Response{JSONRPC: jsonrpcVersion, ID: intID(1)}, step: verdictInitialize, version: handshakeOfferVersion},
		{name: "no response no error", step: verdictFail, errIs: errProtocol},

		// Every non-modern JSON-RPC error code means legacy.
		{name: "method not found", resp: coreTErrResp(codeMethodNotFound, ""), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "invalid params", resp: coreTErrResp(codeInvalidParams, ""), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "invalid request", resp: coreTErrResp(codeInvalidRequest, ""), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "internal error", resp: coreTErrResp(-32603, ""), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "server not initialized", resp: coreTErrResp(-32002, ""), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "implementation-defined -32000", resp: coreTErrResp(-32000, ""), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "code zero", resp: coreTErrResp(0, ""), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "unrelated data.supported is ignored", resp: coreTErrResp(codeInvalidParams, supportedModern), step: verdictInitialize, version: handshakeOfferVersion},

		// -32022 retries discover at a modern version, or initializes at a
		// handshake version, from data.supported.
		{name: "unsupported names modern", resp: coreTErrResp(codeUnsupportedProtocolVersion, supportedModern), step: verdictDiscover, version: modernProtocolVersion},
		{name: "unsupported names modern and handshake", resp: coreTErrResp(codeUnsupportedProtocolVersion, `{"supported":["2026-07-28","2025-11-25"]}`), step: verdictDiscover, version: modernProtocolVersion},
		{name: "unsupported names handshake only", resp: coreTErrResp(codeUnsupportedProtocolVersion, `{"supported":["2025-06-18","2024-11-05"]}`), step: verdictInitialize, version: "2025-06-18"},
		{name: "unsupported names nothing mutual", resp: coreTErrResp(codeUnsupportedProtocolVersion, `{"supported":["2099-01-01"]}`), step: verdictFail, errIs: errIncompatible, errHas: `"2099-01-01"`},
		{name: "unsupported with empty list", resp: coreTErrResp(codeUnsupportedProtocolVersion, `{"supported":[]}`), step: verdictFail, errIs: errIncompatible},
		{name: "unsupported without data", resp: coreTErrResp(codeUnsupportedProtocolVersion, ""), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "unsupported with null data", resp: coreTErrResp(codeUnsupportedProtocolVersion, "null"), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "unsupported with empty data", resp: coreTErrResp(codeUnsupportedProtocolVersion, `{}`), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "unsupported with malformed list", resp: coreTErrResp(codeUnsupportedProtocolVersion, `{"supported":"2026-07-28"}`), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "unsupported with non-string entries", resp: coreTErrResp(codeUnsupportedProtocolVersion, `{"supported":[2026]}`), step: verdictInitialize, version: handshakeOfferVersion},

		// The other modern-only codes fail the attempt.
		{name: "missing client capability", resp: coreTErrResp(codeMissingClientCapability, `{"requiredCapabilities":{"elicitation":{}}}`), step: verdictFail, errIs: errIncompatible, errHas: "capabilities"},
		{name: "header mismatch", resp: coreTErrResp(codeHeaderMismatch, ""), step: verdictFail, errIs: errProtocol, thaneBug: true},

		// Shutdown is never a verdict.
		{name: "cancelled", err: fmt.Errorf("wrap: %w", context.Canceled), step: verdictFail, errIs: context.Canceled},
	}

	for _, transport := range stdioAndHTTP {
		for _, tt := range tests {
			tt.transport = transport
			t.Run(transport+"/"+tt.name, func(t *testing.T) {
				got := classifyDiscover(tt.transport, tt.resp, tt.err)
				coreTCheckVerdict(t, tt, got)
				if got.step == verdictModern {
					if got.discover == nil || got.discover.Capabilities == nil {
						t.Errorf("modern verdict without discover result: %+v", got)
					}
				} else if got.discover != nil {
					t.Errorf("discover set on %v verdict", got.step)
				}
				if got.initialize != nil {
					t.Errorf("initialize set on a discover verdict")
				}
			})
		}
	}

	httpOnly := []coreTVerdictCase{
		// Only 400/404/405 bodies are era evidence.
		{name: "400 without body", err: coreTStatus(400, nil), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "404 without body", err: coreTStatus(404, nil), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "405 without body", err: coreTStatus(405, nil), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "404 with method not found", err: coreTStatus(404, coreTRPC(codeMethodNotFound, "")), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "400 session required", err: coreTStatus(400, coreTRPC(-32000, "")), step: verdictInitialize, version: handshakeOfferVersion},
		{name: "400 unsupported names modern", err: coreTStatus(400, coreTRPC(codeUnsupportedProtocolVersion, supportedModern)), step: verdictDiscover, version: modernProtocolVersion},
		{name: "400 unsupported names handshake", err: coreTStatus(400, coreTRPC(codeUnsupportedProtocolVersion, `{"supported":["2025-03-26"]}`)), step: verdictInitialize, version: "2025-03-26"},
		{name: "400 unsupported disjoint", err: coreTStatus(400, coreTRPC(codeUnsupportedProtocolVersion, `{"supported":["2030-01-01"]}`)), step: verdictFail, errIs: errIncompatible},
		{name: "400 missing capability", err: coreTStatus(400, coreTRPC(codeMissingClientCapability, "")), step: verdictFail, errIs: errIncompatible},
		{name: "400 header mismatch", err: coreTStatus(400, coreTRPC(codeHeaderMismatch, "")), step: verdictFail, errIs: errProtocol, thaneBug: true},
		{name: "wrapped 404", err: fmt.Errorf("post: %w", coreTStatus(404, nil)), step: verdictInitialize, version: handshakeOfferVersion},
		// Never an era verdict.
		{name: "401", err: coreTStatus(401, nil), step: verdictFail},
		{name: "403 with unsupported body", err: coreTStatus(403, coreTRPC(codeUnsupportedProtocolVersion, supportedModern)), step: verdictFail},
		{name: "406", err: coreTStatus(406, nil), step: verdictFail, thaneBug: true},
		{name: "415", err: coreTStatus(415, nil), step: verdictFail, thaneBug: true},
		{name: "408", err: coreTStatus(408, nil), step: verdictFail},
		{name: "429", err: coreTStatus(429, nil), step: verdictFail},
		{name: "500", err: coreTStatus(500, nil), step: verdictFail},
		{name: "502", err: coreTStatus(502, coreTRPC(codeMethodNotFound, "")), step: verdictFail},
		{name: "503", err: coreTStatus(503, nil), step: verdictFail},
		{name: "307 not followed", err: coreTStatus(307, nil), step: verdictFail},
		{name: "409", err: coreTStatus(409, nil), step: verdictFail},
		{name: "422 with unsupported body", err: coreTStatus(422, coreTRPC(codeUnsupportedProtocolVersion, supportedModern)), step: verdictFail},
		{name: "unreachable", err: fmt.Errorf("%w: dial tcp: connection refused", errUnreachable), step: verdictFail, errIs: errUnreachable},
		{name: "connection lost", err: fmt.Errorf("%w: %s", errConnectionLost, connLost), step: verdictFail, errIs: errConnectionLost},
		{name: "protocol", err: fmt.Errorf("%w: text/html", errProtocol), step: verdictFail, errIs: errProtocol},
		{name: "timeout", err: fmt.Errorf("post: %w", context.DeadlineExceeded), step: verdictFail, errIs: context.DeadlineExceeded},
		{name: "other error", err: errors.New("boom"), step: verdictFail},
	}
	for _, tt := range httpOnly {
		tt.transport = transportHTTP
		t.Run("http/"+tt.name, func(t *testing.T) {
			coreTCheckVerdict(t, tt, classifyDiscover(tt.transport, tt.resp, tt.err))
		})
	}

	stdioOnly := []coreTVerdictCase{
		// A timeout is legacy on the same process.
		{name: "timeout", err: fmt.Errorf("%w", context.DeadlineExceeded), step: verdictInitialize, version: handshakeOfferVersion},
		// Exit or EOF during the probe respawns once.
		{name: "connection lost", err: fmt.Errorf("%w: %s", errConnectionLost, connLost), step: verdictRespawn, version: handshakeOfferVersion},
		{name: "unreachable", err: fmt.Errorf("%w: process exited", errUnreachable), step: verdictRespawn, version: handshakeOfferVersion},
		{name: "lost wins over timeout", err: fmt.Errorf("%w: %w", errConnectionLost, context.DeadlineExceeded), step: verdictRespawn, version: handshakeOfferVersion},
		{name: "cancel wins over lost", err: fmt.Errorf("%w: %w", errConnectionLost, context.Canceled), step: verdictFail, errIs: context.Canceled},
		{name: "oversize line", err: fmt.Errorf("%w: message too large", errProtocol), step: verdictFail, errIs: errProtocol},
		{name: "other error", err: errors.New("boom"), step: verdictFail},
	}
	for _, tt := range stdioOnly {
		tt.transport = transportStdio
		t.Run("stdio/"+tt.name, func(t *testing.T) {
			coreTCheckVerdict(t, tt, classifyDiscover(tt.transport, tt.resp, tt.err))
		})
	}
}

func TestProtocol_ClassifyDiscoverModernResult(t *testing.T) {
	resp := coreTResult(`{"supportedVersions":["2026-07-28"],"capabilities":{"tools":{"listChanged":true}},"instructions":"hi","ttlMs":5000,` +
		`"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"s","version":"1"}}}`)
	v := classifyDiscover(transportHTTP, resp, nil)
	if v.step != verdictModern || v.discover == nil {
		t.Fatalf("verdict = %+v", v)
	}
	d := v.discover
	if !d.Capabilities.Tools.ListChanged || d.Instructions != "hi" || d.TTLMs == nil || *d.TTLMs != 5000 {
		t.Errorf("discover = %+v", d)
	}
	if info, ok := d.serverInfo(); !ok || info.Name != "s" {
		t.Errorf("serverInfo = %+v, %v", info, ok)
	}
}

func TestProtocol_ClassifyInitialize(t *testing.T) {
	result := func(v string) *Response {
		return coreTResult(`{"protocolVersion":"` + v + `","capabilities":{"tools":{}},"serverInfo":{"name":"srv","version":"1"}}`)
	}
	tests := []coreTVerdictCase{
		{name: "offered version accepted", resp: result("2025-11-25"), step: verdictLegacy, version: "2025-11-25"},
		{name: "counter-offer 2025-06-18", resp: result("2025-06-18"), step: verdictLegacy, version: "2025-06-18"},
		{name: "counter-offer 2025-03-26", resp: result("2025-03-26"), step: verdictLegacy, version: "2025-03-26"},
		{name: "counter-offer 2024-11-05", resp: result("2024-11-05"), step: verdictLegacy, version: "2024-11-05"},
		{name: "modern version is not a handshake", resp: result("2026-07-28"), step: verdictFail, errIs: errIncompatible, errHas: "2026-07-28"},
		{name: "unknown version", resp: result("1.0"), step: verdictFail, errIs: errIncompatible, errHas: `"1.0"`},
		{name: "missing version", resp: coreTResult(`{"capabilities":{}}`), step: verdictFail, errIs: errIncompatible},
		{name: "huge version is capped", resp: result(strings.Repeat("9", 500)), step: verdictFail, errIs: errIncompatible},
		{name: "malformed result", resp: coreTResult(`{"protocolVersion":5}`), step: verdictFail, errIs: errProtocol},
		{name: "array result", resp: coreTResult(`[]`), step: verdictFail, errIs: errProtocol},
		{name: "no response", step: verdictFail, errIs: errProtocol},

		// -32022 to initialize is modern evidence.
		{name: "unsupported names modern", resp: coreTErrResp(codeUnsupportedProtocolVersion, `{"supported":["2026-07-28"],"requested":"2025-11-25"}`), step: verdictDiscover, version: modernProtocolVersion},
		{name: "unsupported names both prefers modern", resp: coreTErrResp(codeUnsupportedProtocolVersion, `{"supported":["2025-06-18","2026-07-28"]}`), step: verdictDiscover, version: modernProtocolVersion},
		{name: "unsupported names handshake", resp: coreTErrResp(codeUnsupportedProtocolVersion, `{"supported":["2025-06-18"]}`), step: verdictInitialize, version: "2025-06-18"},
		{name: "unsupported disjoint", resp: coreTErrResp(codeUnsupportedProtocolVersion, `{"supported":["3000-01-01"]}`), step: verdictFail, errIs: errIncompatible, errHas: "3000-01-01"},
		{name: "unsupported without data", resp: coreTErrResp(codeUnsupportedProtocolVersion, ""), step: verdictFail, errIs: errIncompatible},
		// Any error naming handshake versions re-offers one of them.
		{name: "spec example -32602", resp: coreTErrResp(codeInvalidParams, `{"supported":["2024-11-05"],"requested":"1.0.0"}`), step: verdictInitialize, version: "2024-11-05"},
		{name: "other code naming several", resp: coreTErrResp(-32000, `{"supported":["2025-03-26","2025-06-18"]}`), step: verdictInitialize, version: "2025-06-18"},
		{name: "non -32022 naming only modern", resp: coreTErrResp(codeInvalidParams, `{"supported":["2026-07-28"]}`), step: verdictFail, errIs: errIncompatible},
		{name: "plain invalid params", resp: coreTErrResp(codeInvalidParams, ""), step: verdictFail, errHas: "rejected: jsonrpc error"},
		{name: "internal error", resp: coreTErrResp(-32603, `{"detail":"x"}`), step: verdictFail, errHas: "rejected: jsonrpc error"},

		// HTTP.
		{name: "400 unsupported names modern", err: coreTStatus(400, coreTRPC(codeUnsupportedProtocolVersion, `{"supported":["2026-07-28"]}`)), step: verdictDiscover, version: modernProtocolVersion},
		{name: "400 naming handshake", err: coreTStatus(400, coreTRPC(codeInvalidParams, `{"supported":["2025-03-26"]}`)), step: verdictInitialize, version: "2025-03-26"},
		{name: "400 json-rpc error", err: coreTStatus(400, coreTRPC(-32000, "")), step: verdictFail, errHas: "rejected: jsonrpc error"},
		{name: "400 non-JSON hints at HTTP+SSE", err: coreTStatus(400, nil), step: verdictFail, errHas: "HTTP+SSE"},
		{name: "404 non-JSON hints at HTTP+SSE", err: coreTStatus(404, nil), step: verdictFail, errHas: "HTTP+SSE"},
		{name: "405 non-JSON hints at HTTP+SSE", err: coreTStatus(405, nil), step: verdictFail, errHas: "HTTP+SSE"},
		{name: "401", err: coreTStatus(401, coreTRPC(codeUnsupportedProtocolVersion, `{"supported":["2026-07-28"]}`)), step: verdictFail},
		{name: "406", err: coreTStatus(406, nil), step: verdictFail, thaneBug: true},
		{name: "415", err: coreTStatus(415, nil), step: verdictFail, thaneBug: true},
		{name: "503", err: coreTStatus(503, nil), step: verdictFail},
		{name: "connection lost", err: fmt.Errorf("%w: eof", errConnectionLost), step: verdictFail, errIs: errConnectionLost},
		{name: "timeout", err: context.DeadlineExceeded, step: verdictFail, errIs: context.DeadlineExceeded},
		{name: "cancelled", err: context.Canceled, step: verdictFail, errIs: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyInitialize(tt.resp, tt.err)
			coreTCheckVerdict(t, tt, got)
			if got.step == verdictLegacy {
				if got.initialize == nil || got.initialize.ServerInfo.Name != "srv" || got.initialize.Capabilities.Tools == nil {
					t.Errorf("legacy verdict initialize = %+v", got.initialize)
				}
			} else if got.initialize != nil {
				t.Errorf("initialize set on %v verdict", got.step)
			}
			if got.discover != nil {
				t.Error("discover set on an initialize verdict")
			}
			if got.err != nil && len(got.err.Error()) > 2048 {
				t.Errorf("err text is %d bytes; server-supplied parts must be capped", len(got.err.Error()))
			}
		})
	}
}

func TestProtocol_ClassifyKeepsRPCErrorReachable(t *testing.T) {
	rpc := coreTRPC(codeMissingClientCapability, "")
	v := classifyDiscover(transportStdio, &Response{Error: rpc}, nil)
	var got *RPCError
	if !errors.As(v.err, &got) || got != rpc {
		t.Errorf("errors.As(*RPCError) through verdict err = %v", v.err)
	}
	statusErr := coreTStatus(503, nil)
	v = classifyInitialize(nil, statusErr)
	var se *httpStatusError
	if !errors.As(v.err, &se) || se.Status != 503 {
		t.Errorf("errors.As(*httpStatusError) through verdict err = %v", v.err)
	}
}

func TestProtocol_VerdictStepString(t *testing.T) {
	tests := map[verdictStep]string{
		verdictFail:       "fail",
		verdictModern:     "modern",
		verdictLegacy:     "legacy",
		verdictInitialize: "initialize",
		verdictDiscover:   "discover",
		verdictRespawn:    "respawn",
		verdictStep(77):   "fail",
	}
	for s, want := range tests {
		if got := s.String(); got != want {
			t.Errorf("verdictStep(%d).String() = %q, want %q", int(s), got, want)
		}
	}
}

func TestProtocol_ParseSupportedVersions(t *testing.T) {
	tests := []struct {
		data string
		want []string
		ok   bool
	}{
		{data: "", ok: false},
		{data: "null", ok: false},
		{data: "{}", ok: false},
		{data: `{"supported":null}`, ok: false},
		{data: `{"supported":"x"}`, ok: false},
		{data: `[1]`, ok: false},
		{data: `{"supported":[]}`, want: []string{}, ok: true},
		{data: `{"supported":["2026-07-28"],"requested":"x"}`, want: []string{"2026-07-28"}, ok: true},
	}
	for _, tt := range tests {
		var data json.RawMessage
		if tt.data != "" {
			data = json.RawMessage(tt.data)
		}
		got, ok := parseSupportedVersions(data)
		if ok != tt.ok || strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("parseSupportedVersions(%s) = %v, %v; want %v, %v", tt.data, got, ok, tt.want, tt.ok)
		}
	}
}

func TestProtocol_UnsupportedVersionsErrIsCapped(t *testing.T) {
	var supported []string
	for i := 0; i < 50; i++ {
		supported = append(supported, strings.Repeat("v", 200))
	}
	err := unsupportedVersionsErr(supported, coreTRPC(codeUnsupportedProtocolVersion, ""))
	if !errors.Is(err, errIncompatible) {
		t.Errorf("err = %v, want errIncompatible", err)
	}
	if strings.Count(err.Error(), strings.Repeat("v", versionCapBytes)) != supportedListCap {
		t.Errorf("err names %d versions, want %d", strings.Count(err.Error(), strings.Repeat("v", versionCapBytes)), supportedListCap)
	}
	if strings.Contains(err.Error(), strings.Repeat("v", versionCapBytes+1)) {
		t.Error("a version was not capped")
	}
	if !strings.Contains(err.Error(), "…") {
		t.Error("a cut list is not marked")
	}
}
