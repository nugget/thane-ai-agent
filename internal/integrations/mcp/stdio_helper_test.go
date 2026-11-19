package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// stdioTHelperEnv selects a fake-server mode when the test binary is
// re-executed as an MCP stdio server.
const stdioTHelperEnv = "MCP_TEST_HELPER"

// TestMain is the package's only TestMain. With MCP_TEST_HELPER=<mode>
// set, the test binary runs as a fake stdio MCP server instead of the
// tests (the helper-process pattern), so stdio tests talk to a real
// child process over real pipes.
func TestMain(m *testing.M) {
	if mode := os.Getenv(stdioTHelperEnv); mode != "" {
		os.Exit(stdioTRunHelper(mode))
	}
	os.Exit(m.Run())
}

// stdioTHelper returns a stdioConfig that runs this test binary as the
// fake server named by mode. Modes other stages may rely on:
//
//   - "mcp-legacy": a 2025-11-25 server. initialize (any handshake
//     version is echoed back, else 2025-11-25), notifications/initialized
//     (required before tools/*), ping, server/discover -> -32601, tools/list
//     in two pages (cursor "page-2"), tools "echo" (returns argument
//     "text") and "sleep" (argument "ms"; honours notifications/cancelled
//     by never replying). Modern _meta keys get -32600.
//   - "mcp-modern": a 2026-07-28 server. Every request needs params._meta
//     with the modern protocolVersion and clientCapabilities (missing ->
//     -32602, another version -> -32022); server/discover; initialize ->
//     -32022; ping -> -32601; the same tools and pages, with resultType.
//
// The stdio tests use further "stdio-*" modes. The waits are shortened
// so a stuck helper costs at most a few seconds; callers may override.
// Under -race the helper's race runtime would sleep 1s at exit
// (atexit_sleep_ms), which makes a clean stdin-EOF exit look like a
// hung server, so the helper runs with that sleep disabled.
func stdioTHelper(t testing.TB, mode string) stdioConfig {
	t.Helper()
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("stdioTHelper: resolve test binary: %v", err)
	}
	gorace := strings.TrimSpace(os.Getenv("GORACE") + " atexit_sleep_ms=0")
	return stdioConfig{
		command:        exe,
		args:           []string{"-test.run=^$"},
		env:            []string{stdioTHelperEnv + "=" + mode, "GORACE=" + gorace},
		logger:         coreTLogger(t),
		closeStdinWait: time.Second,
		termWait:       time.Second,
		exitDrainWait:  time.Second,
	}
}

// stdioTRunHelper runs the fake server for mode and returns the exit
// code.
func stdioTRunHelper(mode string) int {
	switch mode {
	case "stdio-noread": // never reads stdin, never writes
		time.Sleep(time.Minute)
		return 0
	case "stdio-silent-exit": // fails at start-up, only stderr
		fmt.Fprintln(os.Stderr, "fatal: missing API token")
		return 1
	case "stdio-hold": // a grandchild that holds inherited pipes open
		time.Sleep(30 * time.Second)
		return 0
	case "stdio-close-stdin": // stops reading: writes to it get EPIPE
		_ = os.Stdin.Close()
		time.Sleep(time.Minute)
		return 0
	case "stdio-stubborn": // ignores stdin EOF and SIGTERM
		signal.Ignore(syscall.SIGTERM)
	}
	s := &stdioTServer{
		mode:     mode,
		inflight: make(map[string]chan struct{}),
		waiting:  make(map[string]chan json.RawMessage),
		replies:  make(map[string]json.RawMessage),
	}
	s.serve()
	if mode == "stdio-ignore-stdin" || mode == "stdio-stubborn" {
		time.Sleep(time.Minute)
	}
	return 0
}

// stdioTServer is the fake server's state.
type stdioTServer struct {
	mode  string
	outMu sync.Mutex

	mu          sync.Mutex
	initialized bool
	cancelled   []stdioTCancel
	slept       int                             // "sleep" replies sent, including late ones
	inflight    map[string]chan struct{}        // request id → closed on cancel
	waiting     map[string]chan json.RawMessage // server request id → reply
	replies     map[string]json.RawMessage      // every reply the client sent
	held        []json.RawMessage               // ids of "hold" requests
	floodDone   chan struct{}                   // closed when "flood" ends
}

// stdioTCancel is one notifications/cancelled the fake server saw.
type stdioTCancel struct {
	RequestID json.RawMessage `json:"requestId"`
	Reason    string          `json:"reason"`
}

// serve reads stdin until EOF.
func (s *stdioTServer) serve() {
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	for {
		line, err := in.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if msgs, derr := decodeMessages(line); derr == nil {
				for i := range msgs {
					s.handle(&msgs[i])
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// stdioTKey is a map key for a raw JSON-RPC id.
func stdioTKey(id json.RawMessage) string { return string(bytes.TrimSpace(id)) }

func (s *stdioTServer) handle(m *wireMessage) {
	switch m.kind() {
	case kindResponse:
		raw, _ := json.Marshal(m.response())
		key := stdioTKey(m.ID)
		s.mu.Lock()
		s.replies[key] = raw
		ch := s.waiting[key]
		delete(s.waiting, key)
		s.mu.Unlock()
		if ch != nil {
			ch <- raw
		}
	case kindNotification:
		switch m.Method {
		case methodCancelled:
			var c stdioTCancel
			_ = json.Unmarshal(m.Params, &c)
			s.mu.Lock()
			s.cancelled = append(s.cancelled, c)
			if ch, ok := s.inflight[stdioTKey(c.RequestID)]; ok {
				close(ch)
				delete(s.inflight, stdioTKey(c.RequestID))
			}
			s.mu.Unlock()
		case methodInitialized:
			s.mu.Lock()
			s.initialized = true
			s.mu.Unlock()
		}
	case kindRequest:
		req := m.request()
		switch s.mode {
		case "mcp-legacy", "mcp-modern":
			go s.answerMCP(req)
		default:
			go s.answerScript(req)
		}
	}
}

// writeLine writes one raw line to stdout.
func (s *stdioTServer) writeLine(b []byte) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = os.Stdout.Write(append(b, '\n'))
}

// send marshals v as one line.
func (s *stdioTServer) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	s.writeLine(b)
}

// result sends a success response.
func (s *stdioTServer) result(id json.RawMessage, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	s.send(&Response{JSONRPC: jsonrpcVersion, ID: id, Result: raw})
}

// fail sends an error response; data may be nil.
func (s *stdioTServer) fail(id json.RawMessage, code int, msg string, data any) {
	e := &RPCError{Code: code, Message: msg}
	if data != nil {
		e.Data, _ = json.Marshal(data)
	}
	s.send(&Response{JSONRPC: jsonrpcVersion, ID: id, Error: e})
}

// track registers a cancellable request and returns its cancel channel.
func (s *stdioTServer) track(id json.RawMessage) chan struct{} {
	ch := make(chan struct{})
	s.mu.Lock()
	s.inflight[stdioTKey(id)] = ch
	s.mu.Unlock()
	return ch
}

// untrack removes a finished request.
func (s *stdioTServer) untrack(id json.RawMessage) {
	s.mu.Lock()
	delete(s.inflight, stdioTKey(id))
	s.mu.Unlock()
}

// sleep waits ms or until the request is cancelled; true = cancelled.
func (s *stdioTServer) sleep(id json.RawMessage, ms int) bool {
	cancelled := s.track(id)
	defer s.untrack(id)
	timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-cancelled:
		return true
	case <-timer.C:
		return false
	}
}

// --- mcp-legacy / mcp-modern ---

// stdioTTools are the fake MCP tools, one per tools/list page.
var stdioTTools = []string{
	`{"name":"echo","description":"Echo the text argument back.","inputSchema":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}`,
	`{"name":"sleep","description":"Wait ms milliseconds, then report it.","inputSchema":{"type":"object","properties":{"ms":{"type":"integer"}},"required":["ms"]}}`,
}

// stdioTMCPParams is the union of the params the fake MCP modes read.
type stdioTMCPParams struct {
	Meta            map[string]json.RawMessage `json:"_meta"`
	ProtocolVersion string                     `json:"protocolVersion"`
	Cursor          *string                    `json:"cursor"`
	Name            string                     `json:"name"`
	Arguments       struct {
		Text string `json:"text"`
		MS   int    `json:"ms"`
	} `json:"arguments"`
}

func (s *stdioTServer) answerMCP(req *Request) {
	var p stdioTMCPParams
	if len(req.Params) > 0 && json.Unmarshal(req.Params, &p) != nil {
		s.fail(req.ID, codeInvalidParams, "params must be an object", nil)
		return
	}
	modern := s.mode == "mcp-modern"
	known := req.Method == methodToolsList || req.Method == methodToolsCall
	if modern {
		known = known || req.Method == methodDiscover || req.Method == methodInitialize
	} else {
		known = known || req.Method == methodInitialize || req.Method == methodPing
	}
	if !known {
		s.fail(req.ID, codeMethodNotFound, "Method not found", req.Method)
		return
	}
	if modern {
		if req.Method == methodInitialize {
			s.fail(req.ID, codeUnsupportedProtocolVersion, "connection is serving the 2026-07-28 protocol; the initialize handshake is not accepted",
				unsupportedVersionData{Supported: modernVersions, Requested: p.ProtocolVersion})
			return
		}
		var version string
		raw, ok := p.Meta[metaProtocolVersion]
		_, caps := p.Meta[metaClientCapabilities]
		if !ok || !caps || json.Unmarshal(raw, &version) != nil {
			s.fail(req.ID, codeInvalidParams, "missing required _meta "+metaProtocolVersion+" or "+metaClientCapabilities, nil)
			return
		}
		if version != modernProtocolVersion {
			s.fail(req.ID, codeUnsupportedProtocolVersion, "unsupported protocol version",
				unsupportedVersionData{Supported: modernVersions, Requested: version})
			return
		}
	} else {
		for k := range p.Meta {
			if strings.HasPrefix(k, "io.modelcontextprotocol/") {
				s.fail(req.ID, codeInvalidRequest, "modern _meta on a legacy connection", nil)
				return
			}
		}
	}

	switch {
	case !modern && req.Method == methodInitialize:
		version := handshakeOfferVersion
		if isHandshakeVersion(p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		s.result(req.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":      map[string]any{"name": "thane-test-legacy", "version": "1.0.0"},
			"instructions":    "Test legacy server.",
		})
	case modern && req.Method == methodDiscover:
		s.result(req.ID, map[string]any{
			"resultType":        resultTypeComplete,
			"supportedVersions": modernVersions,
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"instructions":      "Test modern server.",
			"_meta":             map[string]any{metaServerInfo: map[string]any{"name": "thane-test-modern", "version": "1.0.0"}},
		})
	case !modern && req.Method == methodPing:
		s.result(req.ID, map[string]any{})
	case req.Method == methodToolsList || req.Method == methodToolsCall:
		s.mu.Lock()
		ready := modern || s.initialized
		s.mu.Unlock()
		if !ready {
			s.fail(req.ID, codeInvalidRequest, "received request before initialization was complete", nil)
			return
		}
		if req.Method == methodToolsList {
			s.toolsList(req.ID, p.Cursor, modern)
		} else {
			s.toolsCall(req.ID, &p, modern)
		}
	default:
		s.fail(req.ID, codeMethodNotFound, "Method not found", req.Method)
	}
}

func (s *stdioTServer) toolsList(id json.RawMessage, cursor *string, modern bool) {
	page := 0
	switch {
	case cursor == nil:
	case *cursor == "page-2":
		page = 1
	default:
		s.fail(id, codeInvalidParams, "invalid cursor", nil)
		return
	}
	res := map[string]any{"tools": []json.RawMessage{json.RawMessage(stdioTTools[page])}}
	if page == 0 {
		res["nextCursor"] = "page-2"
	}
	if modern {
		res["resultType"] = resultTypeComplete
	}
	s.result(id, res)
}

func (s *stdioTServer) toolsCall(id json.RawMessage, p *stdioTMCPParams, modern bool) {
	var text string
	switch p.Name {
	case "echo":
		text = p.Arguments.Text
	case "sleep":
		if s.sleep(id, p.Arguments.MS) {
			return // cancelled: send nothing further for this request
		}
		text = fmt.Sprintf("slept %dms", p.Arguments.MS)
	default:
		s.fail(id, codeInvalidParams, "Unknown tool: "+p.Name, nil)
		return
	}
	res := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	if modern {
		res["resultType"] = resultTypeComplete
	}
	s.result(id, res)
}
