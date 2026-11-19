package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// httpTWait bounds every wait in these tests.
const httpTWait = 5 * time.Second

// httpTRequest is one request a fake server received.
type httpTRequest struct {
	method string // HTTP method
	header http.Header
	msg    wireMessage // decoded JSON-RPC body (zero for DELETE)
}

// httpTServer is an httptest server that records every request and
// hands the decoded JSON-RPC message to handle.
type httpTServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []httpTRequest
}

func httpTStart(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, m *wireMessage)) *httpTServer {
	t.Helper()
	s := &httpTServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m wireMessage
		if msgs, err := decodeMessages(body); err == nil && len(msgs) == 1 {
			m = msgs[0]
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, httpTRequest{method: r.Method, header: r.Header.Clone(), msg: m})
		s.mu.Unlock()
		handle(w, r, &m)
	}))
	t.Cleanup(s.Close)
	return s
}

// requests returns the recorded requests whose JSON-RPC method (or,
// for "DELETE", HTTP method) is name; "" returns all.
func (s *httpTServer) requests(name string) []httpTRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []httpTRequest
	for _, r := range s.reqs {
		if name == "" || r.msg.Method == name || (name == http.MethodDelete && r.method == name) {
			out = append(out, r)
		}
	}
	return out
}

// only returns the single recorded request named name.
func (s *httpTServer) only(t *testing.T, name string) httpTRequest {
	t.Helper()
	got := s.requests(name)
	if len(got) != 1 {
		t.Fatalf("server saw %d %q requests, want 1", len(got), name)
	}
	return got[0]
}

func httpTNew(t *testing.T, url string, headers map[string]string, hooks transportHooks) *httpTransport {
	t.Helper()
	tr, err := newHTTPTransport(t.Context(), httpConfig{url: url, headers: headers, logger: coreTLogger(t)}, hooks)
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close(context.Background()) })
	return tr
}

func httpTJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func httpTResult(id json.RawMessage, result string) string {
	return `{"jsonrpc":"2.0","id":` + string(id) + `,"result":` + result + `}`
}

// httpTStream writes an SSE response, flushing after every write.
type httpTStream struct {
	w http.ResponseWriter
}

func httpTSSE(w http.ResponseWriter) *httpTStream {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	s := &httpTStream{w: w}
	s.flush()
	return s
}

func (s *httpTStream) raw(text string) {
	_, _ = io.WriteString(s.w, text)
	s.flush()
}

func (s *httpTStream) data(msg string) { s.raw("data: " + msg + "\n\n") }

func (s *httpTStream) flush() {
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
}

// httpTBlock waits for the client to abort the request (or the test
// bound) and reports whether the abort was seen.
func httpTBlock(r *http.Request) bool {
	select {
	case <-r.Context().Done():
		return true
	case <-time.After(httpTWait):
		return false
	}
}

// httpTCall runs a Call on the test's ctx and fails the test on error.
func httpTCall(t *testing.T, tr *httpTransport, req *Request, info callInfo) *Response {
	t.Helper()
	resp, err := tr.Call(t.Context(), req, info)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp == nil {
		t.Fatal("Call returned a nil response")
	}
	return resp
}

const httpTInitResult = `{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}`

// TestHTTP_LegacyStatefulSession drives a legacy stateful server end to
// end: initialize over SSE (priming event, comment, notification) that
// assigns a session, notifications/initialized answered 202, a
// tools/call whose SSE stream carries a server ping that must be
// answered by a POSTed reply before the result arrives, and Close
// ending the session with DELETE.
func TestHTTP_LegacyStatefulSession(t *testing.T) {
	const session = "sess-1"
	replied := make(chan struct{})
	var replyOnce sync.Once
	srv := httpTStart(t, func(w http.ResponseWriter, r *http.Request, m *wireMessage) {
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case m.Method == methodInitialize:
			w.Header().Set("mcp-session-id", session) // any case
			s := httpTSSE(w)
			s.raw("id: p1\ndata:\n\n")
			s.raw(": keep-alive\n\n")
			s.data(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"hi"}}`)
			s.data(httpTResult(m.ID, httpTInitResult))
		case m.Method == methodInitialized:
			w.WriteHeader(http.StatusAccepted)
		case m.Method == methodToolsCall:
			s := httpTSSE(w)
			s.data(`{"jsonrpc":"2.0","id":"srv-1","method":"ping"}`)
			select {
			case <-replied:
			case <-time.After(httpTWait):
				return
			}
			s.data(httpTResult(m.ID, `{"content":[{"type":"text","text":"ok"}]}`))
		case m.kind() == kindResponse:
			replyOnce.Do(func() { close(replied) })
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})

	var (
		mu            sync.Mutex
		notifications []string
		requests      []string
	)
	hooks := transportHooks{
		onRequest: func(_ context.Context, req *Request) *Response {
			mu.Lock()
			requests = append(requests, req.Method)
			mu.Unlock()
			return &Response{Result: json.RawMessage(`{}`)}
		},
		onNotification: func(_ context.Context, n *Notification) {
			mu.Lock()
			notifications = append(notifications, n.Method)
			mu.Unlock()
		},
	}
	tr := httpTNew(t, srv.URL, map[string]string{"Authorization": "Bearer tok"}, hooks)
	ctx := t.Context()
	legacy := callInfo{protocolVersion: "2025-11-25", cancellable: true}

	resp := httpTCall(t, tr, &Request{Method: methodInitialize, Params: json.RawMessage(`{"protocolVersion":"2025-11-25"}`)}, callInfo{})
	if !strings.Contains(string(resp.Result), "2025-11-25") {
		t.Fatalf("initialize result = %s", resp.Result)
	}
	if err := tr.Notify(ctx, &Notification{Method: methodInitialized}, callInfo{protocolVersion: "2025-11-25"}); err != nil {
		t.Fatalf("Notify initialized: %v", err)
	}
	resp = httpTCall(t, tr, &Request{Method: methodToolsCall, Params: json.RawMessage(`{"name":"echo","arguments":{}}`)}, legacy)
	if resp.Error != nil || !strings.Contains(string(resp.Result), `"ok"`) {
		t.Fatalf("tools/call response = %+v", resp)
	}
	if err := tr.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-tr.Done():
	default:
		t.Fatal("Done not closed after Close")
	}

	initReq := srv.only(t, methodInitialize)
	for _, h := range []string{httpHeaderProtocolVersion, httpHeaderSessionID, httpHeaderMethod} {
		if v := initReq.header.Get(h); v != "" {
			t.Errorf("initialize carried %s: %q", h, v)
		}
	}
	if got := initReq.header.Get("Accept"); got != httpAcceptValue {
		t.Errorf("Accept = %q", got)
	}
	if got := initReq.header.Get("Content-Type"); got != httpContentJSON {
		t.Errorf("Content-Type = %q", got)
	}
	for _, name := range []string{methodInitialized, methodToolsCall, http.MethodDelete} {
		r := srv.only(t, name)
		if got := r.header.Get(httpHeaderSessionID); got != session {
			t.Errorf("%s Mcp-Session-Id = %q, want %q", name, got, session)
		}
		if got := r.header.Get(httpHeaderProtocolVersion); got != "2025-11-25" {
			t.Errorf("%s MCP-Protocol-Version = %q", name, got)
		}
		if got := r.header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("%s Authorization = %q", name, got)
		}
		if r.header.Get(httpHeaderMethod) != "" || r.header.Get(httpHeaderName) != "" {
			t.Errorf("%s carried modern headers", name)
		}
	}
	var reply *httpTRequest
	for _, r := range srv.requests("") {
		if r.msg.kind() == kindResponse {
			reply = &r
		}
	}
	if reply == nil {
		t.Fatal("server never received the ping reply")
	}
	if string(reply.msg.ID) != `"srv-1"` || string(reply.msg.Result) != `{}` {
		t.Errorf("reply = id %s result %s", reply.msg.ID, reply.msg.Result)
	}
	if reply.header.Get(httpHeaderSessionID) != session || reply.header.Get(httpHeaderProtocolVersion) != "2025-11-25" {
		t.Errorf("reply headers = %v", reply.header)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notifications) != 1 || notifications[0] != methodLogMessage {
		t.Errorf("notifications = %v", notifications)
	}
	if len(requests) != 1 || requests[0] != methodPing {
		t.Errorf("server requests = %v", requests)
	}
}

func TestHTTP_ModernHeaders(t *testing.T) {
	srv := httpTStart(t, func(w http.ResponseWriter, r *http.Request, m *wireMessage) {
		if m.Method == methodInitialize {
			w.Header().Set(httpHeaderSessionID, "legacy-session")
		}
		if m.kind() == kindNotification {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		httpTJSON(w, http.StatusOK, httpTResult(m.ID, `{}`))
	})
	tr := httpTNew(t, srv.URL, nil, transportHooks{})
	ctx := t.Context()

	// A session from an earlier legacy initialize must never reach a
	// modern request.
	httpTCall(t, tr, &Request{Method: methodInitialize}, callInfo{})
	modern := callInfo{
		protocolVersion: modernProtocolVersion,
		modern:          true,
		name:            "café ☕",
		paramHeaders: map[string]string{
			"Region":   "us-west1",
			"Greeting": "Hello, 世界",
			"Bad Name": "skipped",
		},
		cancellable: true,
	}
	httpTCall(t, tr, &Request{Method: methodToolsCall, Params: json.RawMessage(`{"name":"café ☕"}`)}, modern)
	if err := tr.Notify(ctx, &Notification{Method: "notifications/x"}, callInfo{protocolVersion: modernProtocolVersion, modern: true}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	httpTCall(t, tr, &Request{Method: methodToolsList}, callInfo{protocolVersion: "2025-11-25"})

	call := srv.only(t, methodToolsCall)
	want := map[string]string{
		httpHeaderProtocolVersion:          modernProtocolVersion,
		httpHeaderMethod:                   methodToolsCall,
		httpHeaderName:                     "=?base64?" + base64.StdEncoding.EncodeToString([]byte("café ☕")) + "?=",
		httpHeaderParamPrefix + "Region":   "us-west1",
		httpHeaderParamPrefix + "Greeting": "=?base64?SGVsbG8sIOS4lueVjA==?=",
		httpHeaderSessionID:                "",
	}
	for h, v := range want {
		if got := call.header.Get(h); got != v {
			t.Errorf("tools/call %s = %q, want %q", h, got, v)
		}
	}
	for h := range call.header {
		if strings.Contains(h, " ") {
			t.Errorf("invalid header name sent: %q", h)
		}
	}
	note := srv.only(t, "notifications/x")
	if note.header.Get(httpHeaderMethod) != "notifications/x" || note.header.Get(httpHeaderSessionID) != "" {
		t.Errorf("modern notification headers = %v", note.header)
	}
	list := srv.only(t, methodToolsList)
	if list.header.Get(httpHeaderSessionID) != "legacy-session" || list.header.Get(httpHeaderMethod) != "" {
		t.Errorf("legacy request headers = %v", list.header)
	}
}

func TestHTTP_HeaderPrecedence(t *testing.T) {
	srv := httpTStart(t, func(w http.ResponseWriter, _ *http.Request, m *wireMessage) {
		httpTJSON(w, http.StatusOK, httpTResult(m.ID, `{}`))
	})
	configured := map[string]string{
		"Authorization":        "Bearer tok",
		"x-custom":             "v",
		"accept":               "text/plain",
		"Content-Type":         "text/plain",
		"mcp-session-id":       "evil",
		"MCP-Protocol-Version": "1999-01-01",
		"Mcp-Method":           "evil",
		"Mcp-Name":             "evil",
		"mcp-param-region":     "evil",
		"Last-Event-ID":        "9",
		"Bad Header":           "x",
		"X-Bad-Value":          "a\r\nb",
	}
	tr := httpTNew(t, srv.URL, configured, transportHooks{})
	httpTCall(t, tr, &Request{Method: "a"}, callInfo{protocolVersion: "2025-11-25"})
	httpTCall(t, tr, &Request{Method: "b"}, callInfo{})
	httpTCall(t, tr, &Request{Method: "c"}, callInfo{protocolVersion: modernProtocolVersion, modern: true, paramHeaders: map[string]string{"Region": "real"}})

	a, b, c := srv.only(t, "a"), srv.only(t, "b"), srv.only(t, "c")
	for _, r := range []httpTRequest{a, b, c} {
		if r.header.Get("Authorization") != "Bearer tok" || r.header.Get("X-Custom") != "v" {
			t.Errorf("%s lost operator headers: %v", r.msg.Method, r.header)
		}
		if r.header.Get("Accept") != httpAcceptValue || r.header.Get("Content-Type") != httpContentJSON {
			t.Errorf("%s Accept/Content-Type overridden: %v", r.msg.Method, r.header)
		}
		for _, h := range []string{httpHeaderSessionID, httpHeaderLastEventID, "X-Bad-Value"} {
			if v := r.header.Get(h); v != "" {
				t.Errorf("%s carried %s = %q", r.msg.Method, h, v)
			}
		}
	}
	if got := a.header.Get(httpHeaderProtocolVersion); got != "2025-11-25" {
		t.Errorf("a MCP-Protocol-Version = %q", got)
	}
	if got := b.header.Get(httpHeaderProtocolVersion); got != "" {
		t.Errorf("b MCP-Protocol-Version = %q, want none", got)
	}
	for _, r := range []httpTRequest{a, b} {
		if r.header.Get(httpHeaderMethod) != "" || r.header.Get(httpHeaderName) != "" || r.header.Get(httpHeaderParamPrefix+"Region") != "" {
			t.Errorf("%s carried operator-set modern headers: %v", r.msg.Method, r.header)
		}
	}
	if c.header.Get(httpHeaderMethod) != "c" || c.header.Get(httpHeaderParamPrefix+"Region") != "real" || c.header.Get(httpHeaderName) != "" {
		t.Errorf("c modern headers = %v", c.header)
	}
}

func TestHTTP_NotifyAcceptsAny2xx(t *testing.T) {
	tests := []struct {
		name   string
		handle func(w http.ResponseWriter, r *http.Request)
	}{
		{name: "202 empty", handle: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }},
		{name: "204", handle: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }},
		{name: "200 json", handle: func(w http.ResponseWriter, _ *http.Request) { httpTJSON(w, http.StatusOK, `{"ignored":true}`) }},
		{name: "200 open sse stream", handle: func(w http.ResponseWriter, r *http.Request) {
			httpTSSE(w).raw(": open\n\n")
			httpTBlock(r)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httpTStart(t, func(w http.ResponseWriter, r *http.Request, _ *wireMessage) { tt.handle(w, r) })
			tr := httpTNew(t, srv.URL, nil, transportHooks{})
			ctx, cancel := context.WithTimeout(t.Context(), httpTWait)
			defer cancel()
			if err := tr.Notify(ctx, &Notification{Method: methodInitialized}, callInfo{}); err != nil {
				t.Fatalf("Notify: %v", err)
			}
		})
	}
}

func TestHTTP_IDsAreFreshAndCallerRequestUntouched(t *testing.T) {
	srv := httpTStart(t, func(w http.ResponseWriter, _ *http.Request, m *wireMessage) {
		httpTJSON(w, http.StatusOK, httpTResult(m.ID, `{}`))
	})
	tr := httpTNew(t, srv.URL, nil, transportHooks{})
	req := &Request{Method: methodPing}
	for range 3 {
		httpTCall(t, tr, req, callInfo{})
	}
	if req.ID != nil || req.JSONRPC != "" {
		t.Fatalf("caller request mutated: %+v", req)
	}
	got := srv.requests(methodPing)
	for i, r := range got {
		if want := fmt.Sprint(i + 1); string(r.msg.ID) != want || r.msg.JSONRPC != jsonrpcVersion {
			t.Errorf("request %d id = %s jsonrpc = %q, want %s", i, r.msg.ID, r.msg.JSONRPC, want)
		}
	}
}

func TestHTTP_ConcurrentCalls(t *testing.T) {
	srv := httpTStart(t, func(w http.ResponseWriter, _ *http.Request, m *wireMessage) {
		time.Sleep(5 * time.Millisecond)
		if string(m.ID) == "3" {
			s := httpTSSE(w)
			s.data(httpTResult(m.ID, `{"id":`+string(m.ID)+`}`))
			return
		}
		httpTJSON(w, http.StatusOK, httpTResult(m.ID, `{"id":`+string(m.ID)+`}`))
	})
	tr := httpTNew(t, srv.URL, nil, transportHooks{})
	var wg sync.WaitGroup
	var bad atomic.Int32
	for range 16 {
		wg.Go(func() {
			resp, err := tr.Call(t.Context(), &Request{Method: methodPing}, callInfo{})
			if err != nil || string(resp.Result) != `{"id":`+string(resp.ID)+`}` {
				bad.Add(1)
			}
		})
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d calls got the wrong response", bad.Load())
	}
}

func TestHTTP_CloseAbortsInflightAndRefusesNewWork(t *testing.T) {
	aborted := make(chan bool, 1)
	srv := httpTStart(t, func(w http.ResponseWriter, r *http.Request, _ *wireMessage) {
		if r.Method == http.MethodDelete {
			t.Error("DELETE sent without a session")
			return
		}
		aborted <- httpTBlock(r)
	})
	tr := httpTNew(t, srv.URL, nil, transportHooks{})
	errc := make(chan error, 1)
	go func() {
		_, err := tr.Call(t.Context(), &Request{Method: methodToolsCall}, callInfo{cancellable: true})
		errc <- err
	}()
	httpTEventually(t, func() bool { return len(srv.requests(methodToolsCall)) == 1 })
	if err := tr.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, errConnectionLost) || errors.Is(err, context.Canceled) {
			t.Fatalf("in-flight Call err = %v, want errConnectionLost", err)
		}
	case <-time.After(httpTWait):
		t.Fatal("Close did not abort the in-flight Call")
	}
	if !<-aborted {
		t.Error("server never saw the abort")
	}
	if err := tr.Close(t.Context()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := tr.Call(t.Context(), &Request{Method: methodPing}, callInfo{}); !errors.Is(err, errUnreachable) {
		t.Fatalf("Call after Close err = %v, want errUnreachable", err)
	}
	if err := tr.Notify(t.Context(), &Notification{Method: methodInitialized}, callInfo{}); !errors.Is(err, errUnreachable) {
		t.Fatalf("Notify after Close err = %v, want errUnreachable", err)
	}
	if n := len(srv.requests(methodPing)); n != 0 {
		t.Fatalf("server saw %d requests after Close", n)
	}
}

// httpTEventually polls cond until it holds or the test bound passes.
func httpTEventually(t *testing.T, cond func() bool) {
	t.Helper()
	coreTWaitFor(t, httpTWait, "the condition", cond)
}

func TestHTTP_NewTransportRejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "ftp://host/mcp", "/relative", "http://", "::bad"} {
		tr, err := newHTTPTransport(t.Context(), httpConfig{url: u, logger: coreTLogger(t)}, transportHooks{})
		if err == nil || tr != nil {
			t.Errorf("url %q: transport %v, err %v; want an error", u, tr, err)
		}
	}
}
