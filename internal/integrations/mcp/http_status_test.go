package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
)

func TestHTTP_StatusErrors(t *testing.T) {
	bigBody := strings.Repeat("x", 5000)
	tests := []struct {
		name     string
		status   int
		ctype    string
		body     string
		wwwAuth  string
		wantCode int // RPC code; 0 = no RPC
		wantBody string
	}{
		{name: "400 unsupported version", status: 400, ctype: "application/json",
			body:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"Unsupported protocol version","data":{"supported":["2026-07-28"],"requested":"x"}}}`,
			wantCode: codeUnsupportedProtocolVersion},
		{name: "400 header mismatch", status: 400, ctype: "application/json",
			body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32020,"message":"Header mismatch"}}`, wantCode: codeHeaderMismatch},
		{name: "400 plain text", status: 400, ctype: "text/plain", body: "Bad Request: No valid session ID", wantBody: "Bad Request: No valid session ID"},
		{name: "401 bearer challenge", status: 401, wwwAuth: `Bearer resource_metadata="https://example.test/.well-known/oauth-protected-resource"`},
		{name: "403 null-id error", status: 403, ctype: "application/json",
			body: `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"Origin not allowed"}}`, wantCode: codeInvalidRequest},
		{name: "404 method not found without session", status: 404, ctype: "application/json",
			body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`, wantCode: codeMethodNotFound},
		{name: "405", status: 405},
		{name: "406", status: 406},
		{name: "429", status: 429},
		{name: "500 oversized body capped", status: 500, ctype: "text/html", body: bigBody, wantBody: bigBody[:httpLogBodyCap]},
		{name: "502 batch body is not one error", status: 502, ctype: "application/json",
			body: `[{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"a"}},{"jsonrpc":"2.0","id":2,"error":{"code":-2,"message":"b"}}]`},
		{name: "307 without location is a status", status: 307},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httpTStart(t, func(w http.ResponseWriter, _ *http.Request, _ *wireMessage) {
				if tt.ctype != "" {
					w.Header().Set("Content-Type", tt.ctype)
				}
				if tt.wwwAuth != "" {
					w.Header().Set("WWW-Authenticate", tt.wwwAuth)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			tr := httpTNew(t, srv.URL, nil, transportHooks{})
			for _, op := range []string{"call", "notify"} {
				var err error
				if op == "call" {
					var resp *Response
					resp, err = tr.Call(t.Context(), &Request{Method: methodDiscover}, callInfo{protocolVersion: modernProtocolVersion, modern: true})
					if resp != nil {
						t.Fatalf("%s: response %+v alongside a status error", op, resp)
					}
				} else {
					err = tr.Notify(t.Context(), &Notification{Method: methodInitialized}, callInfo{})
				}
				var se *httpStatusError
				if !errors.As(err, &se) {
					t.Fatalf("%s: err = %v, want *httpStatusError", op, err)
				}
				if errors.Is(err, errSessionExpired) {
					t.Errorf("%s: a request without a session reported session expiry", op)
				}
				if se.Status != tt.status || se.WWWAuthenticate != tt.wwwAuth {
					t.Errorf("%s: status %d auth %q", op, se.Status, se.WWWAuthenticate)
				}
				if tt.wantBody != "" && se.Body != tt.wantBody {
					t.Errorf("%s: Body = %.40q (len %d), want len %d", op, se.Body, len(se.Body), len(tt.wantBody))
				}
				if len(se.Body) > httpLogBodyCap {
					t.Errorf("%s: Body len %d exceeds cap", op, len(se.Body))
				}
				switch {
				case tt.wantCode == 0 && se.RPC != nil:
					t.Errorf("%s: RPC = %+v, want none", op, se.RPC)
				case tt.wantCode != 0 && (se.RPC == nil || se.RPC.Code != tt.wantCode):
					t.Errorf("%s: RPC = %+v, want code %d", op, se.RPC, tt.wantCode)
				}
				httpTAssertNoLeak(t, srv.URL, err)
			}
		})
	}
}

// httpTAssertNoLeak checks that neither the transport's error text nor
// the model-facing rendering of it carries the server's address.
func httpTAssertNoLeak(t *testing.T, serverURL string, err error) {
	t.Helper()
	u, _ := url.Parse(serverURL)
	host := u.Hostname()
	var mfe *promptfmt.ModelFacingError
	modelErr := modelFacingCallError("mcp_x_y", err)
	text := modelErr.Error()
	if errors.As(modelErr, &mfe) {
		text = mfe.Text
	}
	if strings.Contains(text, host) || strings.Contains(text, u.Port()) {
		t.Errorf("model-facing text leaks the server address: %q", text)
	}
	var se *httpStatusError
	if errors.As(err, &se) && strings.Contains(se.Error(), host) {
		t.Errorf("httpStatusError text leaks the server address: %q", se.Error())
	}
}

// TestHTTP_SessionExpiry: a request carrying Mcp-Session-Id that is
// answered 404, or 400 with a JSON-RPC error that has no id (the
// TypeScript SDK examples' answer to an unknown session), reports
// errSessionExpired and Close skips the DELETE. Any other 400 is a
// plain status error and the session stays live.
func TestHTTP_SessionExpiry(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		ctype   string
		body    string
		expired bool
	}{
		{name: "404 plain text", status: 404, ctype: "text/plain", body: "session not found", expired: true},
		{name: "404 json", status: 404, ctype: "application/json",
			body: `{"jsonrpc":"2.0","error":{"code":-32001,"message":"Session not found"},"id":null}`, expired: true},
		{name: "400 null-id error", status: 400, ctype: "application/json",
			body: `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Bad Request: No valid session ID provided"},"id":null}`, expired: true},
		{name: "400 absent-id error", status: 400, ctype: "application/json",
			body: `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Bad Request"}}`, expired: true},
		{name: "400 attributed error", status: 400, ctype: "application/json",
			body: `{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"Invalid params"}}`},
		{name: "400 plain text", status: 400, ctype: "text/plain", body: "Bad Request"},
		{name: "403 null-id error", status: 403, ctype: "application/json",
			body: `{"jsonrpc":"2.0","error":{"code":-32600,"message":"Origin not allowed"},"id":null}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var deletes atomic.Int32
			srv := httpTStart(t, func(w http.ResponseWriter, r *http.Request, m *wireMessage) {
				switch {
				case r.Method == http.MethodDelete:
					deletes.Add(1)
				case m.Method == methodInitialize:
					w.Header().Set(httpHeaderSessionID, "s1")
					httpTJSON(w, http.StatusOK, httpTResult(m.ID, httpTInitResult))
				default:
					w.Header().Set("Content-Type", tt.ctype)
					w.WriteHeader(tt.status)
					_, _ = w.Write([]byte(tt.body))
				}
			})
			tr := httpTNew(t, srv.URL, nil, transportHooks{})
			httpTCall(t, tr, &Request{Method: methodInitialize}, callInfo{})

			_, err := tr.Call(t.Context(), &Request{Method: methodToolsList}, callInfo{protocolVersion: "2025-11-25"})
			var se *httpStatusError
			if !errors.As(err, &se) || se.Status != tt.status {
				t.Fatalf("Call err = %v, want a %d status error", err, tt.status)
			}
			if got := errors.Is(err, errSessionExpired); got != tt.expired {
				t.Fatalf("Call err = %v: session expired %v, want %v", err, got, tt.expired)
			}
			err = tr.Notify(t.Context(), &Notification{Method: methodInitialized}, callInfo{})
			if got := errors.Is(err, errSessionExpired); got != tt.expired {
				t.Fatalf("Notify err = %v: session expired %v, want %v", err, got, tt.expired)
			}
			if err := tr.Close(t.Context()); err != nil {
				t.Fatalf("Close: %v", err)
			}
			select {
			case <-tr.Done():
			default:
				t.Fatal("Done not closed after Close")
			}
			want := int32(1)
			if tt.expired {
				want = 0
			}
			if got := deletes.Load(); got != want {
				t.Errorf("DELETE sent %d times, want %d", got, want)
			}
		})
	}
}

func TestHTTP_SessionCaptureAndVersionLatch(t *testing.T) {
	tests := []struct {
		name        string
		sessionHdr  string
		wantSession string
	}{
		{name: "visible ascii", sessionHdr: "abc-123_~!", wantSession: "abc-123_~!"},
		{name: "space rejected", sessionHdr: "a b", wantSession: ""},
		{name: "none", sessionHdr: "", wantSession: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httpTStart(t, func(w http.ResponseWriter, r *http.Request, m *wireMessage) {
				switch {
				case r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				case m.Method == methodInitialize:
					if tt.sessionHdr != "" {
						w.Header().Set(httpHeaderSessionID, tt.sessionHdr)
					}
					httpTJSON(w, http.StatusOK, httpTResult(m.ID, httpTInitResult))
				case m.kind() == kindNotification:
					w.Header().Set(httpHeaderSessionID, "not-from-initialize")
					w.WriteHeader(http.StatusAccepted)
				default:
					w.Header().Set(httpHeaderSessionID, "not-from-initialize")
					httpTJSON(w, http.StatusOK, httpTResult(m.ID, `{}`))
				}
			})
			tr := httpTNew(t, srv.URL, nil, transportHooks{})
			ctx := t.Context()
			// A modern discover probe first: its version must not leak
			// onto the legacy handshake or the DELETE.
			httpTCall(t, tr, &Request{Method: methodDiscover}, callInfo{protocolVersion: modernProtocolVersion, modern: true})
			httpTCall(t, tr, &Request{Method: methodInitialize}, callInfo{protocolVersion: modernProtocolVersion})
			if err := tr.Notify(ctx, &Notification{Method: methodInitialized}, callInfo{}); err != nil {
				t.Fatalf("Notify: %v", err)
			}
			httpTCall(t, tr, &Request{Method: methodToolsList}, callInfo{})
			httpTCall(t, tr, &Request{Method: methodPing}, callInfo{})
			_ = tr.Close(ctx)

			if v := srv.only(t, methodInitialize).header.Get(httpHeaderProtocolVersion); v != "" {
				t.Errorf("initialize carried MCP-Protocol-Version %q", v)
			}
			for _, name := range []string{methodInitialized, methodToolsList, methodPing} {
				r := srv.only(t, name)
				if got := r.header.Get(httpHeaderSessionID); got != tt.wantSession {
					t.Errorf("%s session = %q, want %q", name, got, tt.wantSession)
				}
				if v := r.header.Get(httpHeaderProtocolVersion); v != "" {
					t.Errorf("%s carried MCP-Protocol-Version %q", name, v)
				}
			}
			deletes := srv.requests(http.MethodDelete)
			if tt.wantSession == "" {
				if len(deletes) != 0 {
					t.Fatalf("DELETE sent without a session")
				}
				return
			}
			if len(deletes) != 1 {
				t.Fatalf("DELETEs = %d, want 1", len(deletes))
			}
			if got := deletes[0].header.Get(httpHeaderSessionID); got != tt.wantSession {
				t.Errorf("DELETE session = %q", got)
			}
			if v := deletes[0].header.Get(httpHeaderProtocolVersion); v != "" {
				t.Errorf("DELETE carried the probe's MCP-Protocol-Version %q", v)
			}
		})
	}
}

func TestHTTP_CallerDeadlineAndCancellation(t *testing.T) {
	tests := []struct {
		name       string
		info       callInfo
		cancel     bool // cancel the ctx instead of letting it time out
		wantNotify string
		wantErr    error
	}{
		{name: "legacy timeout notifies", info: callInfo{protocolVersion: "2025-06-18", cancellable: true}, wantNotify: "timeout", wantErr: context.DeadlineExceeded},
		{name: "legacy cancel notifies", info: callInfo{protocolVersion: "2025-06-18", cancellable: true}, cancel: true, wantNotify: "cancelled", wantErr: context.Canceled},
		{name: "legacy non-cancellable stays quiet", info: callInfo{protocolVersion: "2025-06-18"}, wantErr: context.DeadlineExceeded},
		{name: "modern aborts only", info: callInfo{protocolVersion: modernProtocolVersion, modern: true, cancellable: true}, wantErr: context.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			aborted := make(chan bool, 1)
			srv := httpTStart(t, func(w http.ResponseWriter, r *http.Request, m *wireMessage) {
				switch m.Method {
				case methodInitialize:
					w.Header().Set(httpHeaderSessionID, "s1")
					httpTJSON(w, http.StatusOK, httpTResult(m.ID, httpTInitResult))
				case methodCancelled:
					w.WriteHeader(http.StatusAccepted)
				case methodToolsCall:
					httpTSSE(w).raw(": working\n\n")
					aborted <- httpTBlock(r)
				}
			})
			tr := httpTNew(t, srv.URL, nil, transportHooks{})
			httpTCall(t, tr, &Request{Method: methodInitialize}, callInfo{})

			ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
			defer cancel()
			if tt.cancel {
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			_, err := tr.Call(ctx, &Request{Method: methodToolsCall, Params: json.RawMessage(`{"name":"slow"}`)}, tt.info)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if !<-aborted {
				t.Error("the POST was not aborted")
			}
			got := srv.requests(methodCancelled)
			if tt.wantNotify == "" {
				if len(got) != 0 {
					t.Fatalf("sent %d notifications/cancelled, want none", len(got))
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("sent %d notifications/cancelled, want 1", len(got))
			}
			var p cancelledParams
			if err := json.Unmarshal(got[0].msg.Params, &p); err != nil {
				t.Fatalf("cancelled params: %v", err)
			}
			callID := srv.only(t, methodToolsCall).msg.ID
			if string(p.RequestID) != string(callID) || p.Reason != tt.wantNotify {
				t.Errorf("cancelled = %+v, want requestId %s reason %q", p, callID, tt.wantNotify)
			}
			if got[0].header.Get(httpHeaderSessionID) != "s1" || got[0].header.Get(httpHeaderProtocolVersion) != "2025-06-18" {
				t.Errorf("cancelled headers = %v", got[0].header)
			}
		})
	}
}

func TestHTTP_JSONResponses(t *testing.T) {
	tests := []struct {
		name    string
		ctype   string
		status  int
		body    func(id string) string
		max     int
		want    string // expected Result, or Error message when wantRPC
		wantRPC bool
		wantErr error
	}{
		{name: "single", body: func(id string) string { return httpTResult(json.RawMessage(id), `{"a":1}`) }, want: `{"a":1}`},
		{name: "charset parameter", ctype: "Application/JSON; charset=utf-8", body: func(id string) string { return httpTResult(json.RawMessage(id), `{}`) }, want: `{}`},
		{name: "only response with another id", body: func(string) string { return httpTResult(json.RawMessage(`"zzz"`), `{"b":2}`) }, want: `{"b":2}`},
		{name: "batch picks matching id", body: func(id string) string {
			return `[` + httpTResult(json.RawMessage(`999`), `{"wrong":1}`) + `,{"jsonrpc":"2.0","method":"notifications/progress"},` + httpTResult(json.RawMessage(id), `{"right":1}`) + `]`
		}, want: `{"right":1}`},
		{name: "null-id error", body: func(string) string {
			return `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"Parse error"}}`
		}, want: "Parse error", wantRPC: true},
		{name: "rpc error is a response", body: func(id string) string {
			return `{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32601,"message":"Method not found"}}`
		}, want: "Method not found", wantRPC: true},
		{name: "malformed", body: func(string) string { return `{"jsonrpc":` }, wantErr: errProtocol},
		{name: "empty body", body: func(string) string { return `` }, wantErr: errProtocol},
		{name: "notification only", body: func(string) string { return `{"jsonrpc":"2.0","method":"notifications/progress"}` }, wantErr: errProtocol},
		{name: "two unmatched responses", body: func(string) string {
			return `[` + httpTResult(json.RawMessage(`998`), `{}`) + `,` + httpTResult(json.RawMessage(`999`), `{}`) + `]`
		}, wantErr: errProtocol},
		{name: "oversize", max: 64, body: func(id string) string { return httpTResult(json.RawMessage(id), `"`+strings.Repeat("x", 100)+`"`) }, wantErr: errProtocol},
		{name: "unexpected content type", ctype: "text/plain", body: func(string) string { return "hello" }, wantErr: errProtocol},
		{name: "missing content type", ctype: "-", body: func(string) string { return "" }, wantErr: errProtocol},
		{name: "202 for a request", status: http.StatusAccepted, body: func(string) string { return "" }, wantErr: errProtocol},
		{name: "204 for a request", status: http.StatusNoContent, body: func(string) string { return "" }, wantErr: errProtocol},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httpTStart(t, func(w http.ResponseWriter, _ *http.Request, m *wireMessage) {
				switch tt.ctype {
				case "":
					w.Header().Set("Content-Type", "application/json")
				case "-":
					w.Header()["Content-Type"] = nil
				default:
					w.Header().Set("Content-Type", tt.ctype)
				}
				status := tt.status
				if status == 0 {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(tt.body(string(m.ID))))
			})
			tr := httpTNew(t, srv.URL, nil, transportHooks{})
			if tt.max > 0 {
				tr.maxMessage = tt.max
			}
			resp, err := tr.Call(t.Context(), &Request{Method: methodToolsCall}, callInfo{})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) || resp != nil {
					t.Fatalf("resp %+v err = %v, want %v", resp, err, tt.wantErr)
				}
				return
			}
			resp = httpTMustResp(t, resp, err)
			switch {
			case tt.wantRPC && (resp.Error == nil || resp.Error.Message != tt.want):
				t.Fatalf("error = %+v, want message %q", resp.Error, tt.want)
			case !tt.wantRPC && string(resp.Result) != tt.want:
				t.Fatalf("result = %s, want %s", resp.Result, tt.want)
			}
		})
	}
}

func httpTMustResp(t *testing.T, resp *Response, err error) *Response {
	t.Helper()
	if err != nil || resp == nil {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	return resp
}

func TestHTTP_JSONBatchDispatchesInbound(t *testing.T) {
	replied := make(chan string, 1)
	srv := httpTStart(t, func(w http.ResponseWriter, _ *http.Request, m *wireMessage) {
		if m.kind() == kindResponse {
			replied <- string(m.ID)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		httpTJSON(w, http.StatusOK, `[{"jsonrpc":"2.0","method":"notifications/tools/list_changed"},`+
			`{"jsonrpc":"2.0","id":"x","method":"ping"},`+httpTResult(m.ID, `{}`)+`]`)
	})
	notified := make(chan string, 1)
	hooks := transportHooks{
		onRequest:      func(context.Context, *Request) *Response { return &Response{} },
		onNotification: func(_ context.Context, n *Notification) { notified <- n.Method },
	}
	tr := httpTNew(t, srv.URL, nil, hooks)
	httpTCall(t, tr, &Request{Method: methodToolsList}, callInfo{})
	if got := <-notified; got != methodToolsListChanged {
		t.Errorf("notification = %q", got)
	}
	select {
	case id := <-replied:
		if id != `"x"` {
			t.Errorf("reply id = %s", id)
		}
	case <-time.After(httpTWait):
		t.Fatal("no reply POSTed for the batched ping")
	}
}

func TestHTTP_SSEResponses(t *testing.T) {
	tests := []struct {
		name    string
		max     int
		write   func(s *httpTStream, id string)
		want    string
		wantErr error
	}{
		{name: "ends without response", write: func(s *httpTStream, _ string) {
			s.data(`{"jsonrpc":"2.0","method":"notifications/progress"}`)
		}, wantErr: errConnectionLost},
		{name: "other event types and unmatched responses are skipped", write: func(s *httpTStream, id string) {
			s.raw("event: endpoint\ndata: /messages\n\n")
			s.data(`[` + httpTResult(json.RawMessage(`12344`), `{"wrong":1}`) + `,` + httpTResult(json.RawMessage(`12345`), `{"wrong":2}`) + `]`)
			s.raw("id: 3\nretry: 100\ndata:\n\n")
			s.data(httpTResult(json.RawMessage(id), `{"right":1}`))
		}, want: `{"right":1}`},
		{name: "only response with another id", write: func(s *httpTStream, _ string) {
			s.data(httpTResult(json.RawMessage(`"zzz"`), `{"b":2}`))
		}, want: `{"b":2}`},
		{name: "batch event", write: func(s *httpTStream, id string) {
			s.data(`[{"jsonrpc":"2.0","method":"notifications/progress"},` + httpTResult(json.RawMessage(id), `{"b":1}`) + `]`)
		}, want: `{"b":1}`},
		{name: "multi-line data", write: func(s *httpTStream, id string) {
			s.raw("data: {\"jsonrpc\":\"2.0\",\ndata: \"id\":" + id + ",\"result\":{\"m\":1}}\n\n")
		}, want: `{"m":1}`},
		{name: "malformed event", write: func(s *httpTStream, _ string) { s.data(`{not json`) }, wantErr: errProtocol},
		{name: "oversize event", max: 64, write: func(s *httpTStream, id string) {
			s.data(httpTResult(json.RawMessage(id), `"`+strings.Repeat("x", 100)+`"`))
		}, wantErr: errProtocol},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httpTStart(t, func(w http.ResponseWriter, _ *http.Request, m *wireMessage) {
				tt.write(httpTSSE(w), string(m.ID))
			})
			tr := httpTNew(t, srv.URL, nil, transportHooks{})
			if tt.max > 0 {
				tr.maxMessage = tt.max
			}
			resp, err := tr.Call(t.Context(), &Request{Method: methodToolsCall}, callInfo{})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if resp = httpTMustResp(t, resp, err); string(resp.Result) != tt.want {
				t.Fatalf("result = %s, want %s", resp.Result, tt.want)
			}
		})
	}
}

// A server that keeps the stream open after the response must not hold
// the call: the transport stops reading and aborts the stream.
func TestHTTP_SSEStopsAtResponse(t *testing.T) {
	aborted := make(chan bool, 1)
	srv := httpTStart(t, func(w http.ResponseWriter, r *http.Request, m *wireMessage) {
		httpTSSE(w).data(httpTResult(m.ID, `{}`))
		aborted <- httpTBlock(r)
	})
	tr := httpTNew(t, srv.URL, nil, transportHooks{})
	start := time.Now()
	httpTCall(t, tr, &Request{Method: methodToolsCall}, callInfo{})
	if d := time.Since(start); d > httpTWait/2 {
		t.Fatalf("Call took %v", d)
	}
	if !<-aborted {
		t.Fatal("stream was not aborted after the response")
	}
}

// A lone response whose id does not match (here the request's id as a
// string) still answers the call, as it would in a JSON body: the stream
// belongs to this one request. The server keeps the stream open, so a
// dropped response would hold the call until its deadline.
func TestHTTP_SSELoneResponseWithOtherID(t *testing.T) {
	aborted := make(chan bool, 1)
	srv := httpTStart(t, func(w http.ResponseWriter, r *http.Request, m *wireMessage) {
		httpTSSE(w).data(httpTResult(json.RawMessage(`"`+string(m.ID)+`"`), `{"s":1}`))
		aborted <- httpTBlock(r)
	})
	logs := &coreTLogBuffer{}
	tr, err := newHTTPTransport(t.Context(), httpConfig{url: srv.URL, logger: logs.logger()}, transportHooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close(context.Background())
	ctx, cancel := context.WithTimeout(t.Context(), httpTWait/2)
	defer cancel()
	resp, err := tr.Call(ctx, &Request{Method: methodToolsCall}, callInfo{})
	if resp = httpTMustResp(t, resp, err); string(resp.Result) != `{"s":1}` {
		t.Fatalf("result = %s", resp.Result)
	}
	if !<-aborted {
		t.Fatal("stream was not aborted after the response")
	}
	if !strings.Contains(logs.String(), "accepting the only response") {
		t.Errorf("the id mismatch was not logged: %s", logs.String())
	}
}

// An SSE response stream that the server ends a moment after the
// response (as the spec says it SHOULD) leaves the HTTP/1.1 connection
// reusable: the transport gives the stream a short time to end instead
// of aborting it at once, which would close the connection.
func TestHTTP_SSEEndedStreamReusesConnection(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		msgs, err := decodeMessages(body)
		if err != nil || len(msgs) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		httpTSSE(w).data(httpTResult(msgs[0].ID, `{}`))
		time.Sleep(2 * time.Millisecond) // the end of the stream trails the event, as over a network
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	tr := httpTNew(t, srv.URL, nil, transportHooks{})
	const calls = 10
	for range calls {
		httpTCall(t, tr, &Request{Method: methodToolsCall}, callInfo{})
	}
	// One connection serves them all; a scheduling hiccup past the tail
	// wait may cost one or two more. Aborting every stream costs one each.
	if n := conns.Load(); n > 3 {
		t.Errorf("%d calls used %d connections, want the connection reused", calls, n)
	}
}

// The MCP client has no whole-request or response-header timeout: a
// server slower than such a timer still answers. The contrast case
// shows the stock header timeout (shortened) would have cut it off.
func TestHTTP_NoClientWideTimeouts(t *testing.T) {
	c := newHTTPClient()
	if c.Timeout != 0 || c.CheckRedirect == nil {
		t.Fatalf("client Timeout %v, CheckRedirect set %v", c.Timeout, c.CheckRedirect != nil)
	}
	ic, ok := c.Transport.(httpIdleCloser)
	if !ok || ic.base.ResponseHeaderTimeout != 0 {
		t.Fatalf("transport %T, want httpIdleCloser with ResponseHeaderTimeout 0", c.Transport)
	}

	const slow = 300 * time.Millisecond
	srv := httpTStart(t, func(w http.ResponseWriter, r *http.Request, m *wireMessage) {
		select {
		case <-time.After(slow):
		case <-r.Context().Done():
			return
		}
		httpTJSON(w, http.StatusOK, httpTResult(m.ID, `{}`))
	})
	tr := httpTNew(t, srv.URL, nil, transportHooks{})
	httpTCall(t, tr, &Request{Method: methodToolsCall}, callInfo{})

	short := httpkit.NewTransport()
	short.ResponseHeaderTimeout = slow / 3
	strict, err := newHTTPTransport(t.Context(), httpConfig{url: srv.URL, logger: coreTLogger(t),
		client: httpkit.NewClient(httpkit.WithTransport(short), httpkit.WithTimeout(0))}, transportHooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer strict.Close(context.Background())
	if _, err := strict.Call(t.Context(), &Request{Method: methodToolsCall}, callInfo{}); err == nil {
		t.Fatal("a header timeout shorter than the server should have failed the call")
	}

	// The caller's ctx is the bound.
	ctx, cancel := context.WithTimeout(t.Context(), slow/3)
	defer cancel()
	if _, err := tr.Call(ctx, &Request{Method: methodToolsCall}, callInfo{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestHTTP_Unreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	tr := httpTNew(t, "http://"+addr+"/mcp", nil, transportHooks{})
	_, err = tr.Call(t.Context(), &Request{Method: methodDiscover}, callInfo{})
	if !errors.Is(err, errUnreachable) {
		t.Fatalf("Call err = %v, want errUnreachable", err)
	}
	if err := tr.Notify(t.Context(), &Notification{Method: methodInitialized}, callInfo{}); !errors.Is(err, errUnreachable) {
		t.Fatalf("Notify err = %v, want errUnreachable", err)
	}
	httpTAssertNoLeak(t, "http://"+addr, err)
}
