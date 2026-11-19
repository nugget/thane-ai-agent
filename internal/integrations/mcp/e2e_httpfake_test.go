package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Styles of e2eTHTTP, the end-to-end Streamable HTTP fake server.
const (
	// e2eTStyleStateful is a legacy (2025-11-25) stateful server like the
	// TypeScript SDK's: requests without a session other than initialize
	// get 400 with a -32000 body and a null id; initialize assigns
	// Mcp-Session-Id; responses come as SSE streams that also carry
	// notifications and server requests; an unknown session gets 404;
	// DELETE ends a session.
	e2eTStyleStateful = "stateful"
	// e2eTStyleStatefulTS is e2eTStyleStateful answering an unknown
	// session the way the TypeScript SDK's server examples do: 400 with
	// a -32000 body and a null id, instead of the spec's 404.
	e2eTStyleStatefulTS = "stateful-ts"
	// e2eTStyleStateless is a legacy stateless server: JSON responses,
	// no session, and server/discover answered 200 with -32601.
	e2eTStyleStateless = "stateless"
	// e2eTStyleModern is a 2026-07-28 server that enforces the modern
	// framing: MCP-Protocol-Version must match the _meta version,
	// Mcp-Method the body method, Mcp-Name the tool name, and
	// Mcp-Param-Region the x-mcp-header argument; a mismatch is 400 with
	// -32020 (and a recorded violation).
	e2eTStyleModern = "modern"
)

// e2eTHTTPTools are the tools e2eTHTTP lists. "badheader" has an
// invalid x-mcp-header annotation (not a token), so a modern HTTP
// client must drop it.
var e2eTHTTPTools = []string{
	`{"name":"echo","description":"Echo text.","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}}`,
	`{"name":"sleep","description":"Wait until cancelled.","inputSchema":{"type":"object"}}`,
	`{"name":"weather","description":"Weather for a region.","inputSchema":{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Region"}},"required":["region"]}}`,
	`{"name":"badheader","description":"Invalid annotation.","inputSchema":{"type":"object","properties":{"q":{"type":"string","x-mcp-header":"bad name"}}}}`,
}

// e2eTHTTP is a scripted Streamable HTTP MCP server for the end-to-end
// tests. It records what the client sent and every framing violation.
type e2eTHTTP struct {
	*httptest.Server
	style string

	mu           sync.Mutex
	status       int             // non-zero: every request gets this status (auth failure)
	sessions     map[string]bool // live legacy sessions
	issued       int             // sessions issued so far
	seen         map[string]int  // JSON-RPC method, "reply" or "DELETE" → count
	violations   []string
	cancelled    []json.RawMessage            // requestIds of notifications/cancelled
	aborted      int                          // "sleep" POSTs the client aborted
	waiting      map[string]chan *wireMessage // server request id → the client's reply
	asked        int                          // server requests sent
	failDiscover int                          // modern: discovers still to reject with -32022
	failCall     int                          // modern: tools/calls still to reject with -32020
}

func e2eTHTTPStart(t *testing.T, style string) *e2eTHTTP {
	t.Helper()
	s := &e2eTHTTP{
		style:    style,
		sessions: map[string]bool{},
		seen:     map[string]int{},
		waiting:  map[string]chan *wireMessage{},
	}
	s.Server = httptest.NewServer(s)
	t.Cleanup(s.Close)
	return s
}

// factory builds real HTTP transports for this server's current URL.
func (s *e2eTHTTP) factory(t *testing.T, headers map[string]string) transportFactory {
	logger := coreTLogger(t)
	url := s.URL + "/mcp"
	return func(ctx context.Context, hooks transportHooks) (Transport, error) {
		tr, err := newHTTPTransport(ctx, httpConfig{url: url, headers: headers, logger: logger}, hooks)
		if err != nil {
			return nil, err
		}
		return tr, nil
	}
}

func (s *e2eTHTTP) setStatus(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = code
}

// expireSessions forgets every session (a server restart).
func (s *e2eTHTTP) expireSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.sessions)
}

// script makes the modern style reject the next discovers with -32022
// and the next tool calls with -32020.
func (s *e2eTHTTP) script(failDiscover, failCall int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failDiscover, s.failCall = failDiscover, failCall
}

// snapshot returns a copy of the request counts.
func (s *e2eTHTTP) snapshot() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.seen)
}

// abortedCalls returns how many held calls the client aborted.
func (s *e2eTHTTP) abortedCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.aborted
}

func (s *e2eTHTTP) count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[name]
}

func (s *e2eTHTTP) violated(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.violations = append(s.violations, fmt.Sprintf(format, args...))
}

// checkClean fails the test on any recorded framing violation.
func (s *e2eTHTTP) checkClean(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.violations {
		t.Errorf("server saw a framing violation: %s", v)
	}
}

func (s *e2eTHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	status := s.status
	s.mu.Unlock()
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var m wireMessage
	if msgs, err := decodeMessages(body); err == nil && len(msgs) == 1 {
		m = msgs[0]
	}
	name := m.Method
	switch {
	case r.Method == http.MethodDelete:
		name = http.MethodDelete
	case m.kind() == kindResponse:
		name = "reply"
	}
	s.mu.Lock()
	s.seen[name]++
	s.mu.Unlock()

	if status != 0 {
		w.Header().Set(httpHeaderWWWAuthenticate, `Bearer realm="e2e"`)
		http.Error(w, "unauthorized", status)
		return
	}
	if r.Method == http.MethodDelete {
		s.serveDelete(w, r)
		return
	}
	if r.Method != http.MethodPost {
		s.violated("%s request", r.Method)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get(httpHeaderContentType) != httpContentJSON || r.Header.Get(httpHeaderAccept) != httpAcceptValue {
		s.violated("%s: Content-Type %q, Accept %q", name, r.Header.Get(httpHeaderContentType), r.Header.Get(httpHeaderAccept))
	}
	switch s.style {
	case e2eTStyleStateful, e2eTStyleStatefulTS:
		s.serveStateful(w, r, &m)
	case e2eTStyleStateless:
		s.serveStateless(w, r, &m)
	default:
		s.serveModern(w, r, &m)
	}
}

func (s *e2eTHTTP) serveDelete(w http.ResponseWriter, r *http.Request) {
	sid := r.Header.Get(httpHeaderSessionID)
	s.mu.Lock()
	live := (s.style == e2eTStyleStateful || s.style == e2eTStyleStatefulTS) && s.sessions[sid]
	delete(s.sessions, sid)
	s.mu.Unlock()
	if !live {
		s.violated("DELETE for session %q", sid)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// checkLegacyFrame records modern framing on a legacy request.
func (s *e2eTHTTP) checkLegacyFrame(r *http.Request, m *wireMessage, wantVersion string) {
	if got := r.Header.Get(httpHeaderProtocolVersion); got != wantVersion {
		s.violated("%s: MCP-Protocol-Version %q, want %q", m.Method, got, wantVersion)
	}
	if r.Header.Get(httpHeaderMethod) != "" || r.Header.Get(httpHeaderName) != "" {
		s.violated("%s: modern Mcp-Method/Mcp-Name headers on a legacy request", m.Method)
	}
	if strings.Contains(string(m.Params), "io.modelcontextprotocol/") {
		s.violated("%s: modern _meta on a legacy request", m.Method)
	}
}

func (s *e2eTHTTP) serveStateful(w http.ResponseWriter, r *http.Request, m *wireMessage) {
	sid := r.Header.Get(httpHeaderSessionID)
	const noSession = `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Bad Request: No valid session ID provided"},"id":null}`
	if sid == "" {
		if m.Method != methodInitialize {
			httpTJSON(w, http.StatusBadRequest, noSession)
			return
		}
		s.checkLegacyFrame(r, m, "")
		s.mu.Lock()
		s.issued++
		sid = fmt.Sprintf("sess-%d", s.issued)
		s.sessions[sid] = true
		s.mu.Unlock()
		w.Header().Set(httpHeaderSessionID, sid)
		st := httpTSSE(w)
		st.raw("id: prime-1\ndata:\n\n") // 2025-11-25 priming event
		st.data(httpTResult(m.ID, httpTInitResult))
		return
	}
	s.mu.Lock()
	live := s.sessions[sid]
	s.mu.Unlock()
	switch {
	case !live && s.style == e2eTStyleStatefulTS:
		httpTJSON(w, http.StatusBadRequest, noSession)
		return
	case !live:
		httpTJSON(w, http.StatusNotFound, `{"jsonrpc":"2.0","error":{"code":-32001,"message":"Session not found"},"id":null}`)
		return
	}
	if m.Method == methodInitialize {
		s.violated("initialize on live session %s", sid)
	}
	if m.kind() != kindResponse {
		s.checkLegacyFrame(r, m, handshakeOfferVersion)
	}
	s.serveLegacy(w, r, m, true)
}

func (s *e2eTHTTP) serveStateless(w http.ResponseWriter, r *http.Request, m *wireMessage) {
	if sid := r.Header.Get(httpHeaderSessionID); sid != "" {
		s.violated("%s: Mcp-Session-Id %q on a sessionless server", m.Method, sid)
	}
	switch m.Method {
	case methodDiscover:
		httpTJSON(w, http.StatusOK, `{"jsonrpc":"2.0","id":`+string(m.ID)+`,"error":{"code":-32601,"message":"Method not found"}}`)
	case methodInitialize:
		s.checkLegacyFrame(r, m, "")
		httpTJSON(w, http.StatusOK, httpTResult(m.ID, httpTInitResult))
	default:
		if m.kind() != kindResponse {
			s.checkLegacyFrame(r, m, handshakeOfferVersion)
		}
		s.serveLegacy(w, r, m, false)
	}
}

// serveLegacy answers a legacy request after its framing was checked.
// With sse, tools/list and echo answer on an SSE stream; echo first
// sends a notification and a ping request on it and waits for the
// client's POSTed reply.
func (s *e2eTHTTP) serveLegacy(w http.ResponseWriter, r *http.Request, m *wireMessage, sse bool) {
	switch m.kind() {
	case kindNotification:
		if m.Method == methodCancelled {
			var p cancelledParams
			_ = json.Unmarshal(m.Params, &p)
			s.mu.Lock()
			s.cancelled = append(s.cancelled, p.RequestID)
			s.mu.Unlock()
		}
		w.WriteHeader(http.StatusAccepted)
		return
	case kindResponse:
		s.mu.Lock()
		ch := s.waiting[string(m.ID)]
		delete(s.waiting, string(m.ID))
		s.mu.Unlock()
		if ch == nil {
			s.violated("reply to unknown server request %s", m.ID)
		} else {
			ch <- m
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	_ = json.Unmarshal(m.Params, &p)
	switch {
	case m.Method == methodPing:
		httpTJSON(w, http.StatusOK, httpTResult(m.ID, `{}`))
	case m.Method == methodToolsList:
		res := httpTResult(m.ID, `{"tools":[`+e2eTHTTPTools[0]+`,`+e2eTHTTPTools[1]+`]}`)
		if !sse {
			httpTJSON(w, http.StatusOK, res)
			return
		}
		st := httpTSSE(w)
		st.data(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"listing"}}`)
		st.data(res)
	case m.Method == methodToolsCall && p.Name == "sleep":
		if sse { // cancelled mid-stream, after the response headers
			httpTSSE(w).data(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":2,"progress":0}}`)
		}
		s.block(r)
	case m.Method == methodToolsCall && p.Name == "echo":
		text, _ := p.Arguments["text"].(string)
		res := httpTResult(m.ID, `{"content":[{"type":"text","text":`+e2eTQuote(text)+`}]}`)
		if !sse {
			httpTJSON(w, http.StatusOK, res)
			return
		}
		st := httpTSSE(w)
		st.data(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":1,"progress":0.5}}`)
		if reply := s.ask(st, methodPing); reply == nil || reply.Error != nil || string(reply.Result) != `{}` {
			s.violated("ping on the echo stream was not answered with {}")
		}
		if reply := s.ask(st, methodRootsList); reply == nil || reply.Error == nil || reply.Error.Code != codeMethodNotFound {
			s.violated("roots/list on the echo stream was not answered with -32601")
		}
		st.data(res)
	default:
		httpTJSON(w, http.StatusOK, `{"jsonrpc":"2.0","id":`+string(m.ID)+`,"error":{"code":-32601,"message":"Method not found"}}`)
	}
}

// ask sends a server request on an SSE stream and waits for the reply
// the client POSTs back.
func (s *e2eTHTTP) ask(st *httpTStream, method string) *wireMessage {
	s.mu.Lock()
	s.asked++
	id := fmt.Sprintf(`"srv-%d"`, s.asked)
	ch := make(chan *wireMessage, 1)
	s.waiting[id] = ch
	s.mu.Unlock()
	st.data(`{"jsonrpc":"2.0","id":` + id + `,"method":"` + method + `"}`)
	select {
	case m := <-ch:
		return m
	case <-time.After(httpTWait):
		return nil
	}
}

// block holds a request until the client aborts it.
func (s *e2eTHTTP) block(r *http.Request) {
	if !httpTBlock(r) {
		s.violated("a cancelled call's POST was never aborted")
		return
	}
	s.mu.Lock()
	s.aborted++
	s.mu.Unlock()
}

func (s *e2eTHTTP) serveModern(w http.ResponseWriter, r *http.Request, m *wireMessage) {
	if sid := r.Header.Get(httpHeaderSessionID); sid != "" {
		s.violated("%s: Mcp-Session-Id on a modern request", m.Method)
	}
	if m.kind() != kindRequest {
		s.violated("modern client sent a %s", m.kind())
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var p struct {
		Meta      map[string]json.RawMessage `json:"_meta"`
		Name      string                     `json:"name"`
		Arguments map[string]any             `json:"arguments"`
	}
	_ = json.Unmarshal(m.Params, &p)
	var version string
	_ = json.Unmarshal(p.Meta[metaProtocolVersion], &version)
	_, caps := p.Meta[metaClientCapabilities]
	mismatch := func(what string) {
		s.violated("%s: %s", m.Method, what)
		httpTJSON(w, http.StatusBadRequest, `{"jsonrpc":"2.0","id":`+string(m.ID)+`,"error":{"code":-32020,"message":"Header mismatch: `+what+`"}}`)
	}
	switch {
	case version != modernProtocolVersion || !caps:
		mismatch("missing or wrong _meta envelope")
		return
	case r.Header.Get(httpHeaderProtocolVersion) != version:
		mismatch("MCP-Protocol-Version differs from _meta")
		return
	case r.Header.Get(httpHeaderMethod) != m.Method:
		mismatch("Mcp-Method differs from the body")
		return
	case m.Method == methodToolsCall && e2eTDecodeHeader(r.Header.Get(httpHeaderName)) != p.Name:
		mismatch("Mcp-Name differs from params.name")
		return
	case m.Method == methodToolsCall && p.Name == "weather" &&
		e2eTDecodeHeader(r.Header.Get(httpHeaderParamPrefix+"Region")) != fmt.Sprint(p.Arguments["region"]):
		mismatch("Mcp-Param-Region differs from arguments.region")
		return
	}
	s.mu.Lock()
	rejectDiscover := m.Method == methodDiscover && s.failDiscover > 0
	rejectCall := m.Method == methodToolsCall && s.failCall > 0
	if rejectDiscover {
		s.failDiscover--
	}
	if rejectCall {
		s.failCall--
	}
	s.mu.Unlock()
	switch {
	case rejectDiscover:
		httpTJSON(w, http.StatusBadRequest, `{"jsonrpc":"2.0","id":`+string(m.ID)+`,"error":{"code":-32022,"message":"Unsupported protocol version","data":{"supported":["2026-07-28"],"requested":"2026-07-28"}}}`)
	case rejectCall:
		httpTJSON(w, http.StatusBadRequest, `{"jsonrpc":"2.0","id":`+string(m.ID)+`,"error":{"code":-32020,"message":"Header mismatch (scripted)"}}`)
	case m.Method == methodDiscover:
		httpTJSON(w, http.StatusOK, httpTResult(m.ID, `{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"e2e-modern","version":"2"}}}`))
	case m.Method == methodToolsList:
		httpTJSON(w, http.StatusOK, httpTResult(m.ID, `{"resultType":"complete","tools":[`+strings.Join(e2eTHTTPTools, ",")+`]}`))
	case m.Method == methodToolsCall && p.Name == "sleep":
		httpTSSE(w).raw(": still working\n\n") // cancelled mid-stream
		s.block(r)
	case m.Method == methodToolsCall && (p.Name == "echo" || p.Name == "weather"):
		text, _ := p.Arguments["text"].(string)
		if p.Name == "weather" {
			text = fmt.Sprintf("weather in %v", p.Arguments["region"])
		}
		st := httpTSSE(w)
		st.data(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":1,"progress":1}}`)
		st.data(httpTResult(m.ID, `{"resultType":"complete","content":[{"type":"text","text":`+e2eTQuote(text)+`}]}`))
	case m.Method == methodInitialize:
		httpTJSON(w, http.StatusBadRequest, `{"jsonrpc":"2.0","id":`+string(m.ID)+`,"error":{"code":-32022,"message":"initialize is not accepted","data":{"supported":["2026-07-28"]}}}`)
	default:
		httpTJSON(w, http.StatusOK, `{"jsonrpc":"2.0","id":`+string(m.ID)+`,"error":{"code":-32601,"message":"Method not found"}}`)
	}
}

// e2eTDecodeHeader undoes encodeHeaderValue's base64 sentinel.
func e2eTDecodeHeader(v string) string {
	if !strings.HasPrefix(v, headerB64Prefix) || !strings.HasSuffix(v, headerB64Suffix) {
		return v
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(v, headerB64Prefix), headerB64Suffix))
	if err != nil {
		return "(bad base64)"
	}
	return string(b)
}

// e2eTQuote returns s as a JSON string literal.
func e2eTQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
