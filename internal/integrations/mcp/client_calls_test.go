package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// clientTCursor returns the cursor in params and whether one was sent.
func clientTCursor(params json.RawMessage) (string, bool) {
	var p struct {
		Cursor *string `json:"cursor"`
	}
	if len(params) == 0 || json.Unmarshal(params, &p) != nil || p.Cursor == nil {
		return "", false
	}
	return *p.Cursor, true
}

func clientTStr(s string) *string { return &s }

// clientTFetch negotiates on srv and runs one full tools/list.
func clientTFetch(t *testing.T, srv *clientTFakeServer, transport string) (*serverClient, *toolSet, error) {
	t.Helper()
	c := clientTNew(t, srv, clientTConfig(transport, ""))
	conn, err := c.negotiate(t.Context())
	if err != nil {
		t.Fatalf("negotiate: %v", err)
	}
	t.Cleanup(func() { c.closeConn(conn) })
	set, err := c.fetchTools(t.Context(), conn)
	return c, set, err
}

func TestClient_ListToolsPagination(t *testing.T) {
	pages := func(_ context.Context, _ *clientTFakeTransport, req *Request, _ callInfo) (*Response, error) {
		cursor, sent := clientTCursor(req.Params)
		switch {
		case !sent:
			return clientTToolsPage(clientTStr(""), clientTTool("a"), clientTTool("b")), nil
		case cursor == "": // an empty cursor is a valid cursor
			return clientTToolsPage(clientTStr("p3"), clientTTool("c"), `{"name":"a","description":"duplicate"}`), nil
		case cursor == "p3":
			return clientTToolsPage(nil, `{"name":5}`, `{"description":"no name"}`, `{"name":"bad","inputSchema":"x"}`, clientTTool("d")), nil
		}
		return clientTRPCErr(codeInvalidParams, ""), nil
	}
	for _, tt := range []struct {
		name   string
		routes clientTRoutes
		guard  func(clientTHandler) clientTHandler
		modern bool
	}{
		{name: "legacy", routes: clientTLegacyRoutes(""), guard: clientTLegacyGuard},
		{name: "modern", routes: clientTModernRoutes(), guard: clientTModernGuard, modern: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.routes[methodToolsList] = tt.guard(pages)
			srv := &clientTFakeServer{handle: tt.routes.handler()}
			_, set, err := clientTFetch(t, srv, transportStdio)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, d := range set.defs {
				names = append(names, d.Name)
			}
			if !slices.Equal(names, []string{"a", "b", "c", "d"}) {
				t.Errorf("tools = %q, want a b c d (deduped first-wins, malformed skipped)", names)
			}
			if set.defs[0].Description != "fake tool a" {
				t.Errorf("duplicate replaced the first definition: %q", set.defs[0].Description)
			}
			var cursors []string
			for _, m := range srv.messages() {
				if m.method != methodToolsList {
					continue
				}
				c, sent := clientTCursor(m.params)
				if !sent {
					c = "<none>"
				}
				cursors = append(cursors, c)
				if tt.modern != clientTHasModernMeta(m.params) {
					t.Errorf("tools/list params %s: modern _meta present = %v, want %v", m.params, !tt.modern, tt.modern)
				}
			}
			if !slices.Equal(cursors, []string{"<none>", "", "p3"}) {
				t.Errorf("cursors = %q", cursors)
			}
		})
	}
}

func TestClient_ListToolsStops(t *testing.T) {
	var n atomic.Int64
	cases := []struct {
		name      string
		list      clientTHandler
		wantCalls int
		wantTools int
		wantErr   error
	}{
		{name: "null nextCursor ends the list",
			list:      clientTReply(clientTOK(`{"tools":[`+clientTTool("a")+`],"nextCursor":null}`), nil),
			wantCalls: 1, wantTools: 1},
		{name: "repeated cursor stops", list: clientTReply(clientTToolsPage(clientTStr("same"), clientTTool("a")), nil),
			wantCalls: 2, wantTools: 1},
		{name: "endless pagination stops at the page bound",
			list: func(context.Context, *clientTFakeTransport, *Request, callInfo) (*Response, error) {
				i := n.Add(1)
				return clientTToolsPage(clientTStr(fmt.Sprint(i)), clientTTool(fmt.Sprint("t", i))), nil
			},
			wantCalls: maxListPages, wantTools: maxListPages},
		{name: "-32601 means no tools", list: clientTReply(clientTRPCErr(codeMethodNotFound, ""), nil), wantCalls: 1},
		{name: "-32601 as an HTTP 404 body means no tools",
			list: clientTReply(nil, clientTStatus(404, clientTRPC(codeMethodNotFound, ""))), wantCalls: 1},
		{name: "other JSON-RPC errors fail", list: clientTReply(clientTRPCErr(-32603, ""), nil),
			wantCalls: 1, wantErr: &RPCError{}},
		{name: "malformed result fails", list: clientTReply(clientTOK(`{"tools":5}`), nil),
			wantCalls: 1, wantErr: errProtocol},
		{name: "unexpected resultType fails", list: clientTReply(clientTOK(`{"resultType":"task","tools":[]}`), nil),
			wantCalls: 1, wantErr: errProtocol},
		{name: "transport error fails", list: clientTReply(nil, errConnectionLost), wantCalls: 1, wantErr: errConnectionLost},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			routes := clientTLegacyRoutes("")
			routes[methodToolsList] = tt.list
			srv := &clientTFakeServer{handle: routes.handler()}
			_, set, err := clientTFetch(t, srv, transportStdio)
			if got := srv.count(methodToolsList); got != tt.wantCalls {
				t.Errorf("tools/list calls = %d, want %d", got, tt.wantCalls)
			}
			switch target := tt.wantErr.(type) {
			case nil:
				if err != nil {
					t.Fatalf("fetchTools: %v", err)
				}
				if set.defs == nil || len(set.defs) != tt.wantTools {
					t.Errorf("tools = %d (nil %v), want %d", len(set.defs), set.defs == nil, tt.wantTools)
				}
			case *RPCError:
				if !errors.As(err, &target) {
					t.Errorf("err = %v, want a JSON-RPC error", err)
				}
			default:
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("err = %v, want %v", err, tt.wantErr)
				}
			}
		})
	}
}

func TestClient_ListToolsTTL(t *testing.T) {
	cases := []struct {
		name   string
		routes clientTRoutes
		pages  []string
		want   time.Duration
	}{
		{name: "modern takes the minimum across pages", routes: clientTModernRoutes(),
			pages: []string{`{"tools":[],"ttlMs":5000,"nextCursor":"2"}`, `{"tools":[],"ttlMs":3000}`}, want: 3 * time.Second},
		{name: "modern absent ttl counts as zero", routes: clientTModernRoutes(),
			pages: []string{`{"tools":[],"ttlMs":5000,"nextCursor":"2"}`, `{"tools":[]}`}, want: 0},
		{name: "modern negative ttl counts as zero", routes: clientTModernRoutes(),
			pages: []string{`{"tools":[],"ttlMs":-5}`}, want: 0},
		{name: "legacy has no ttl", routes: clientTLegacyRoutes(""),
			pages: []string{`{"tools":[],"ttlMs":5000}`}, want: 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			handlers := make([]clientTHandler, len(tt.pages))
			for i, p := range tt.pages {
				handlers[i] = clientTReply(clientTOK(p), nil)
			}
			tt.routes[methodToolsList] = clientTSeq(handlers...)
			srv := &clientTFakeServer{handle: tt.routes.handler()}
			_, set, err := clientTFetch(t, srv, transportStdio)
			if err != nil {
				t.Fatal(err)
			}
			if set.ttl != tt.want {
				t.Errorf("ttl = %v, want %v", set.ttl, tt.want)
			}
		})
	}
}

func TestClient_NoteAccessRefreshesStaleModernLists(t *testing.T) {
	cases := []struct {
		name      string
		routes    clientTRoutes
		ttlMs     int64
		floor     time.Duration
		wait      time.Duration
		wantLists int
	}{
		{name: "modern list older than the floor refreshes", routes: clientTModernRoutes(), floor: 30 * time.Millisecond,
			wait: 60 * time.Millisecond, wantLists: 2},
		{name: "fresh modern list does not refresh", routes: clientTModernRoutes(), floor: time.Hour, wantLists: 1},
		{name: "ttl above the floor wins", routes: clientTModernRoutes(), ttlMs: 60_000, floor: time.Millisecond,
			wait: 30 * time.Millisecond, wantLists: 1},
		{name: "legacy lists never refresh on access", routes: clientTLegacyRoutes(""), floor: time.Millisecond,
			wait: 30 * time.Millisecond, wantLists: 1},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			page := fmt.Sprintf(`{"tools":[%s],"ttlMs":%d}`, clientTTool("a"), tt.ttlMs)
			tt.routes[methodToolsList] = clientTReply(clientTOK(page), nil)
			srv := &clientTFakeServer{handle: tt.routes.handler()}
			cfg := clientTConfig(transportStdio, "")
			cfg.listRefreshFloor = tt.floor
			c := clientTNew(t, srv, cfg)
			clientTStart(t, c)
			clientTReadyConn(t, c)
			time.Sleep(tt.wait)
			for range 5 { // repeated accesses coalesce into one refresh
				c.noteAccess()
			}
			if tt.wantLists > 1 {
				clientTEventually(t, "refresh", func() bool { return srv.count(methodToolsList) == tt.wantLists })
			}
			time.Sleep(30 * time.Millisecond)
			if got := srv.count(methodToolsList); got != tt.wantLists {
				t.Errorf("tools/list calls = %d, want %d", got, tt.wantLists)
			}
		})
	}
}

// clientTCallCase scripts tools/call on a ready legacy (or modern) client.
type clientTCallCase struct {
	name     string
	modern   bool
	call     clientTHandler
	list     clientTHandler // optional tools/list override
	tool     string         // default "echo"
	args     map[string]any
	wantText string
	isError  bool
	wantErr  error // errors.Is target; *RPCError: errors.As
	wantCode int   // with wantErr *RPCError
	// wantStatus: the error must still carry this HTTP status (logs).
	wantStatus int
	calls      int // expected tools/call count; 0 = skip, -1 = none
	lists      int // expected tools/list count; 0 = skip
}

func clientTRunCall(t *testing.T, tt clientTCallCase) *clientTFakeServer {
	t.Helper()
	routes := clientTLegacyRoutes("", clientTTool("echo"), clientTTool("other"))
	guard := clientTLegacyGuard
	if tt.modern {
		routes, guard = clientTModernRoutes(clientTTool("echo"), clientTTool("other")), clientTModernGuard
	}
	if tt.call != nil {
		routes[methodToolsCall] = guard(tt.call)
	}
	if tt.list != nil {
		routes[methodToolsList] = guard(tt.list)
	}
	srv := &clientTFakeServer{handle: routes.handler()}
	c := clientTNew(t, srv, clientTConfig(transportHTTP, ""))
	clientTStart(t, c)
	clientTReadyConn(t, c)
	tool := tt.tool
	if tool == "" {
		tool = "echo"
	}
	res, err := c.callTool(t.Context(), tool, tt.args)
	switch target := tt.wantErr.(type) {
	case nil:
		if err != nil {
			t.Fatalf("callTool: %v", err)
		}
		if res.Text != tt.wantText || res.IsError != tt.isError {
			t.Errorf("result = %+v, want text %q isError %v", res, tt.wantText, tt.isError)
		}
	case *RPCError:
		if !errors.As(err, &target) || target.Code != tt.wantCode {
			t.Errorf("err = %v, want JSON-RPC error %d", err, tt.wantCode)
		}
	default:
		if !errors.Is(err, tt.wantErr) {
			t.Errorf("err = %v, want %v", err, tt.wantErr)
		}
	}
	if tt.wantStatus != 0 {
		var statusErr *httpStatusError
		if !errors.As(err, &statusErr) || statusErr.Status != tt.wantStatus {
			t.Errorf("err = %v, want it to keep HTTP status %d", err, tt.wantStatus)
		}
	}
	if tt.calls != 0 {
		if got := srv.count(methodToolsCall); got != max(tt.calls, 0) {
			t.Errorf("tools/call sent %d times, want %d", got, max(tt.calls, 0))
		}
	}
	if tt.lists > 0 {
		if got := srv.count(methodToolsList); got != tt.lists {
			t.Errorf("tools/list sent %d times, want %d", got, tt.lists)
		}
	}
	return srv
}

func TestClient_CallToolResults(t *testing.T) {
	ok := func(result string) clientTHandler { return clientTReply(clientTOK(result), nil) }
	state := func(s string) clientTHandler {
		return ok(`{"resultType":"input_required","requestState":"` + s + `"}`)
	}
	cases := []clientTCallCase{
		{name: "complete result renders text", call: ok(`{"content":[{"type":"text","text":"hello"}]}`), wantText: "hello", calls: 1},
		{name: "explicit complete resultType", modern: true,
			call: ok(`{"resultType":"complete","content":[{"type":"text","text":"hi"}]}`), wantText: "hi"},
		{name: "structured content is preferred",
			call:     ok(`{"content":[{"type":"text","text":"{\"a\": 1}"}],"structuredContent":{"a": 1}}`),
			wantText: `{"a":1}`},
		{name: "isError is a tool-reported failure", call: ok(`{"content":[{"type":"text","text":"bad input"}],"isError":true}`),
			wantText: "bad input", isError: true},
		{name: "empty result", call: ok(`{"content":[]}`), wantText: emptyResultText},
		{name: "inputRequests need a person", modern: true,
			call:    ok(`{"resultType":"input_required","inputRequests":{"k":{"method":"elicitation/create"}}}`),
			wantErr: errInputRequired, calls: 1},
		{name: "requestState-only is continued", modern: true,
			call:     clientTSeq(state("s1"), state("s2"), ok(`{"content":[{"type":"text","text":"done"}]}`)),
			wantText: "done", calls: 3},
		{name: "requestState continuation is bounded", modern: true, call: state("again"),
			wantErr: errProtocol, calls: 1 + maxInputRequiredRounds},
		{name: "input_required with neither field is invalid", modern: true, call: ok(`{"resultType":"input_required"}`),
			wantErr: errProtocol, calls: 1},
		{name: "task result is never an empty success", modern: true,
			call: ok(`{"resultType":"task","task":{"taskId":"t"}}`), wantErr: errProtocol},
		{name: "unknown resultType is invalid", call: ok(`{"resultType":"whatever","content":[]}`), wantErr: errProtocol},
		{name: "malformed result", call: ok(`{"content":"nope"}`), wantErr: errProtocol},
		{name: "unknown tool fails without I/O", tool: "missing", wantErr: errToolGone, calls: -1, lists: 1},
		{name: "connection lost is never re-issued", call: clientTReply(nil, fmt.Errorf("%w: reset", errConnectionLost)),
			wantErr: errConnectionLost, calls: 1},
		{name: "server error passes through", call: clientTReply(clientTRPCErr(-32000, ""), nil),
			wantErr: &RPCError{}, wantCode: -32000, calls: 1, lists: 1},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) { clientTRunCall(t, tt) })
	}
}

func TestClient_CallToolRequestStateIsSentBack(t *testing.T) {
	var seen []string
	var n atomic.Int64
	call := func(_ context.Context, _ *clientTFakeTransport, req *Request, _ callInfo) (*Response, error) {
		var p struct {
			RequestState *string        `json:"requestState"`
			Arguments    map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.RequestState != nil {
			seen = append(seen, *p.RequestState)
		}
		if n.Add(1) == 1 {
			return clientTOK(`{"resultType":"input_required","requestState":"opaque-1"}`), nil
		}
		if p.Arguments["q"] != "x" {
			return clientTRPCErr(codeInvalidParams, ""), nil
		}
		return clientTOK(`{"content":[{"type":"text","text":"ok"}]}`), nil
	}
	srv := clientTRunCall(t, clientTCallCase{modern: true, call: call, args: map[string]any{"q": "x"}, wantText: "ok", calls: 2})
	if !slices.Equal(seen, []string{"opaque-1"}) {
		t.Errorf("requestState sent back = %q", seen)
	}
	var ids []int64
	for _, m := range srv.messages() {
		if m.method == methodToolsCall {
			ids = append(ids, m.id)
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Errorf("continuation ids = %v, want two distinct ids", ids)
	}
}

func TestClient_CallToolSendsAnObject(t *testing.T) {
	srv := clientTRunCall(t, clientTCallCase{wantText: "echo echo {}"})
	for _, m := range srv.messages() {
		if m.method == methodToolsCall && !strings.Contains(string(m.params), `"arguments":{}`) {
			t.Errorf("nil arguments sent as %s, want {}: the schema has arguments?: object, so strict servers reject null", m.params)
		}
	}
}

func TestClient_CallToolRelists(t *testing.T) {
	gone := clientTReply(clientTToolsPage(nil, clientTTool("other")), nil)
	cases := []clientTCallCase{
		{name: "-32602 with the tool still listed returns the server error",
			call: clientTReply(clientTRPCErr(codeInvalidParams, ""), nil), wantErr: &RPCError{}, wantCode: codeInvalidParams,
			calls: 1, lists: 2},
		{name: "-32602 for a tool that disappeared is tool gone",
			call:    clientTReply(clientTRPCErr(codeInvalidParams, ""), nil),
			list:    clientTSeq(clientTReply(clientTToolsPage(nil, clientTTool("echo")), nil), gone),
			wantErr: errToolGone, calls: 1, lists: 2},
		{name: "-32601 for a tool that disappeared is tool gone",
			call:    clientTReply(clientTRPCErr(codeMethodNotFound, ""), nil),
			list:    clientTSeq(clientTReply(clientTToolsPage(nil, clientTTool("echo")), nil), gone),
			wantErr: errToolGone, calls: 1, lists: 2},
		{name: "HeaderMismatch re-lists and retries once", modern: true,
			call:     clientTSeq(clientTReply(nil, clientTStatus(400, clientTRPC(codeHeaderMismatch, ""))), clientTEcho),
			wantText: "echo echo {}", calls: 2, lists: 2},
		{name: "HeaderMismatch twice fails", modern: true,
			call:    clientTReply(nil, clientTStatus(400, clientTRPC(codeHeaderMismatch, ""))),
			wantErr: &RPCError{}, wantCode: codeHeaderMismatch, calls: 2, lists: 2},
		{name: "HeaderMismatch for a tool that disappeared is tool gone", modern: true,
			call:    clientTReply(nil, clientTStatus(400, clientTRPC(codeHeaderMismatch, ""))),
			list:    clientTSeq(clientTReply(clientTToolsPage(nil, clientTTool("echo")), nil), gone),
			wantErr: errToolGone, calls: 1, lists: 2},
		// Modern Streamable HTTP sends JSON-RPC errors with a matching
		// HTTP status; they mean what they mean in a 200 body.
		{name: "modern HTTP 400 carrying -32602 for a tool that disappeared is tool gone", modern: true,
			call:    clientTReply(nil, clientTStatus(400, clientTRPC(codeInvalidParams, ""))),
			list:    clientTSeq(clientTReply(clientTToolsPage(nil, clientTTool("echo")), nil), gone),
			wantErr: errToolGone, calls: 1, lists: 2},
		{name: "modern HTTP 404 carrying -32601 for a tool that disappeared is tool gone", modern: true,
			call:    clientTReply(nil, clientTStatus(404, clientTRPC(codeMethodNotFound, ""))),
			list:    clientTSeq(clientTReply(clientTToolsPage(nil, clientTTool("echo")), nil), gone),
			wantErr: errToolGone, calls: 1, lists: 2},
		{name: "modern HTTP 400 carrying -32602 with the tool still listed returns the server error", modern: true,
			call:    clientTReply(nil, clientTStatus(400, clientTRPC(codeInvalidParams, ""))),
			wantErr: &RPCError{}, wantCode: codeInvalidParams, wantStatus: 400, calls: 1, lists: 2},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) { clientTRunCall(t, tt) })
	}
}

// TestClient_CallToolRelayedDecline: a legacy tool that asks Thane for
// input (elicitation, sampling, roots) gets Thane's decline, and many
// servers relay it as the tools/call error. That means the tool needs
// input Thane cannot give: no re-list, and not "change the arguments".
func TestClient_CallToolRelayedDecline(t *testing.T) {
	reply := func(code int, msg string) clientTHandler {
		return clientTReply(&Response{Error: &RPCError{Code: code, Message: msg}}, nil)
	}
	cases := []clientTCallCase{
		{name: "relayed elicitation decline", call: reply(codeInvalidParams, declineElicitation),
			wantErr: errInputRequired, calls: 1, lists: 1},
		{name: "relayed sampling decline", call: reply(codeMethodNotFound, declineSampling),
			wantErr: errInputRequired, calls: 1, lists: 1},
		{name: "relayed roots decline with a server prefix", call: reply(codeMethodNotFound, "Error: "+declineRoots),
			wantErr: errInputRequired, calls: 1, lists: 1},
		{name: "-32602 with another message still re-lists", call: reply(codeInvalidParams, "region is required"),
			wantErr: &RPCError{}, wantCode: codeInvalidParams, calls: 1, lists: 2},
		{name: "a decline under another code is the server's error", call: reply(-32603, declineElicitation),
			wantErr: &RPCError{}, wantCode: -32603, calls: 1, lists: 1},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) { clientTRunCall(t, tt) })
	}

	err := fmt.Errorf("%w: tool echo: the server relayed Thane's refusal: %w", errInputRequired,
		&RPCError{Code: codeInvalidParams, Message: declineElicitation})
	text, _ := coreTModelText(t, modelFacingCallError("mcp_s_echo", err))
	if text != textInputRequired {
		t.Errorf("model text = %q, want textInputRequired", text)
	}
}

func TestClient_CallToolSessionExpired(t *testing.T) {
	expired := fmt.Errorf("%w: %w", errSessionExpired, clientTStatus(404, nil))
	t.Run("re-initializes and retries once", func(t *testing.T) {
		routes := clientTLegacyRoutes("", clientTTool("echo"))
		routes[methodToolsCall] = func(ctx context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error) {
			if tr.spawn == 1 {
				return nil, expired
			}
			return clientTEcho(ctx, tr, req, info)
		}
		srv := &clientTFakeServer{handle: routes.handler()}
		c := clientTNew(t, srv, clientTConfig(transportHTTP, ""))
		clientTStart(t, c)
		clientTReadyConn(t, c)
		res, err := c.callTool(t.Context(), "echo", map[string]any{"text": "x"})
		if err != nil || res.Text != `echo echo {"text":"x"}` {
			t.Fatalf("callTool = %+v, %v", res, err)
		}
		if srv.spawns() != 2 || srv.count(methodToolsCall) != 2 {
			t.Errorf("spawns = %d, calls = %d; want 2, 2", srv.spawns(), srv.count(methodToolsCall))
		}
	})
	t.Run("expiring again is not retried twice", func(t *testing.T) {
		routes := clientTLegacyRoutes("", clientTTool("echo"))
		routes[methodToolsCall] = clientTReply(nil, expired)
		srv := &clientTFakeServer{handle: routes.handler()}
		c := clientTNew(t, srv, clientTConfig(transportHTTP, ""))
		clientTStart(t, c)
		clientTReadyConn(t, c)
		if _, err := c.callTool(t.Context(), "echo", nil); !errors.Is(err, errSessionExpired) {
			t.Fatalf("err = %v, want errSessionExpired", err)
		}
		if srv.count(methodToolsCall) != 2 {
			t.Errorf("calls = %d, want 2", srv.count(methodToolsCall))
		}
	})
	t.Run("reconnect failure is unavailable", func(t *testing.T) {
		routes := clientTLegacyRoutes("", clientTTool("echo"))
		routes[methodToolsCall] = clientTReply(nil, expired)
		srv := &clientTFakeServer{
			handle: routes.handler(),
			dialErr: func(spawn int) error {
				if spawn > 1 {
					return errUnreachable
				}
				return nil
			},
		}
		c := clientTNew(t, srv, clientTConfig(transportHTTP, ""))
		clientTStart(t, c)
		clientTReadyConn(t, c)
		_, err := c.callTool(t.Context(), "echo", nil)
		if !errors.Is(err, errSessionExpired) || !errors.Is(err, errNotReady) {
			t.Fatalf("err = %v, want errSessionExpired wrapping errNotReady", err)
		}
		if srv.count(methodToolsCall) != 1 {
			t.Errorf("calls = %d, want 1", srv.count(methodToolsCall))
		}
	})
}

func TestClient_CallTimeout(t *testing.T) {
	cases := []struct {
		name        string
		callTimeout time.Duration
		ctxTimeout  time.Duration // 0 = no caller deadline
		want        time.Duration // the deadline the call should see
	}{
		{name: "default applies without a caller deadline", callTimeout: 80 * time.Millisecond, want: 80 * time.Millisecond},
		{name: "earlier caller deadline wins", callTimeout: time.Hour, ctxTimeout: 80 * time.Millisecond, want: 80 * time.Millisecond},
		{name: "later caller deadline is capped", callTimeout: 80 * time.Millisecond, ctxTimeout: time.Hour, want: 80 * time.Millisecond},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var left atomic.Int64
			routes := clientTLegacyRoutes("", clientTTool("slow"))
			routes[methodToolsCall] = func(ctx context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error) {
				if d, ok := ctx.Deadline(); ok {
					left.Store(int64(time.Until(d)))
				}
				return clientTHang(ctx, tr, req, info)
			}
			srv := &clientTFakeServer{handle: routes.handler()}
			cfg := clientTConfig(transportStdio, "")
			cfg.callTimeout = tt.callTimeout
			c := clientTNew(t, srv, cfg)
			clientTStart(t, c)
			clientTReadyConn(t, c)
			ctx := t.Context()
			if tt.ctxTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.ctxTimeout)
				defer cancel()
			}
			_, err := c.callTool(ctx, "slow", nil)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want DeadlineExceeded", err)
			}
			if got := time.Duration(left.Load()); got <= 0 || got > tt.want {
				t.Errorf("deadline seen by the call = %v, want <= %v", got, tt.want)
			}
			for _, m := range srv.messages() {
				if m.method == methodToolsCall && !m.info.cancellable {
					t.Error("tools/call must be cancellable")
				}
			}
			if _, err := c.readyConn(); err != nil {
				t.Errorf("a timed-out call tore the connection down: %v", err)
			}
		})
	}
}

func TestClient_CallToolParamHeaders(t *testing.T) {
	const tool = `{"name":"sql","inputSchema":{"type":"object","properties":{` +
		`"region":{"type":"string","x-mcp-header":"Region"},` +
		`"opts":{"type":"object","properties":{"limit":{"type":"integer","x-mcp-header":"Limit"},"dry":{"type":"boolean","x-mcp-header":"Dry"}}},` +
		`"query":{"type":"string"}}}}`
	const badTool = `{"name":"bad","inputSchema":{"type":"object","properties":{"n":{"type":"number","x-mcp-header":"N"}}}}`
	cases := []struct {
		name      string
		transport string
		modern    bool
		args      map[string]any
		want      map[string]string
		wantName  string
		wantTools []string
	}{
		{name: "modern HTTP mirrors annotated arguments", transport: transportHTTP, modern: true,
			args:     map[string]any{"region": "us-west1", "opts": map[string]any{"limit": float64(42), "dry": true}, "query": "x"},
			want:     map[string]string{"Region": "us-west1", "Limit": "42", "Dry": "true"},
			wantName: "sql", wantTools: []string{"sql"}},
		{name: "null and absent arguments are omitted", transport: transportHTTP, modern: true,
			args:     map[string]any{"region": nil},
			wantName: "sql", wantTools: []string{"sql"}},
		{name: "modern stdio sends no param headers", transport: transportStdio, modern: true,
			args:     map[string]any{"region": "us-west1"},
			wantName: "sql", wantTools: []string{"sql", "bad"}},
		{name: "legacy HTTP sends neither Mcp-Name nor param headers", transport: transportHTTP,
			args: map[string]any{"region": "us-west1"}, wantTools: []string{"sql"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			routes := clientTLegacyRoutes("", tool, badTool)
			if tt.modern {
				routes = clientTModernRoutes(tool, badTool)
			}
			srv := &clientTFakeServer{handle: routes.handler()}
			c := clientTNew(t, srv, clientTConfig(tt.transport, ""))
			clientTStart(t, c)
			clientTReadyConn(t, c)
			if got := clientTToolNames(c); !slices.Equal(got, tt.wantTools) {
				t.Errorf("tools = %q, want %q", got, tt.wantTools)
			}
			if _, err := c.callTool(t.Context(), "sql", tt.args); err != nil {
				t.Fatal(err)
			}
			msgs := srv.messages()
			info := msgs[len(msgs)-1].info
			if info.name != tt.wantName {
				t.Errorf("Mcp-Name = %q, want %q", info.name, tt.wantName)
			}
			if len(info.paramHeaders) != len(tt.want) {
				t.Fatalf("param headers = %v, want %v", info.paramHeaders, tt.want)
			}
			for k, v := range tt.want {
				if info.paramHeaders[k] != v {
					t.Errorf("Mcp-Param-%s = %q, want %q", k, info.paramHeaders[k], v)
				}
			}
		})
	}
}
