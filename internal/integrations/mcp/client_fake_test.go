package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// clientTHandler answers one request on the in-memory fake transport. It
// runs in its own goroutine and must return once ctx is done.
type clientTHandler func(ctx context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error)

// clientTMsg is one outbound message the client sent.
type clientTMsg struct {
	spawn  int
	call   bool // false: notification
	id     int64
	method string
	params json.RawMessage
	info   callInfo
}

// clientTFakeServer scripts an MCP server behind in-memory transports.
// Each factory call is a new "process" (spawn 1, 2, ...).
type clientTFakeServer struct {
	handle  clientTHandler
	notify  func(tr *clientTFakeTransport, n *Notification, info callInfo) error // optional
	dialErr func(spawn int) error                                                // optional

	mu         sync.Mutex
	dials      int
	transports []*clientTFakeTransport
}

// factory returns the transportFactory the client uses.
func (s *clientTFakeServer) factory() transportFactory {
	return func(ctx context.Context, hooks transportHooks) (Transport, error) {
		s.mu.Lock()
		s.dials++
		spawn := s.dials
		dialErr := s.dialErr
		s.mu.Unlock()
		if dialErr != nil {
			if err := dialErr(spawn); err != nil {
				return nil, err
			}
		}
		lifeCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		tr := &clientTFakeTransport{srv: s, spawn: spawn, hooks: hooks, lifeCtx: lifeCtx, cancel: cancel, done: make(chan struct{})}
		s.mu.Lock()
		s.transports = append(s.transports, tr)
		s.mu.Unlock()
		return tr, nil
	}
}

// spawns returns how many transports were built successfully.
func (s *clientTFakeServer) spawns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.transports)
}

// last returns the newest transport (nil before the first).
func (s *clientTFakeServer) last() *clientTFakeTransport {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.transports) == 0 {
		return nil
	}
	return s.transports[len(s.transports)-1]
}

// transportAt returns the i-th transport (0-based).
func (s *clientTFakeServer) transportAt(i int) *clientTFakeTransport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transports[i]
}

// messages returns every outbound message, transport by transport.
func (s *clientTFakeServer) messages() []clientTMsg {
	s.mu.Lock()
	trs := append([]*clientTFakeTransport(nil), s.transports...)
	s.mu.Unlock()
	var out []clientTMsg
	for _, tr := range trs {
		out = append(out, tr.sent()...)
	}
	return out
}

// methods returns the outbound method names in order.
func (s *clientTFakeServer) methods() []string {
	var out []string
	for _, m := range s.messages() {
		out = append(out, m.method)
	}
	return out
}

// count returns how many requests (not notifications) used method.
func (s *clientTFakeServer) count(method string) int {
	n := 0
	for _, m := range s.messages() {
		if m.call && m.method == method {
			n++
		}
	}
	return n
}

// clientTFakeTransport is one in-memory connection.
type clientTFakeTransport struct {
	srv     *clientTFakeServer
	spawn   int
	hooks   transportHooks
	lifeCtx context.Context
	cancel  context.CancelFunc

	nextID   atomic.Int64
	done     chan struct{}
	doneOnce sync.Once
	closed   atomic.Bool

	mu   sync.Mutex
	msgs []clientTMsg
}

var _ Transport = (*clientTFakeTransport)(nil)

func (t *clientTFakeTransport) record(m clientTMsg) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m.spawn = t.spawn
	t.msgs = append(t.msgs, m)
}

func (t *clientTFakeTransport) sent() []clientTMsg {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]clientTMsg(nil), t.msgs...)
}

// Call fails the way real transports do: a closed transport is unreachable, a
// lost one mid-call is connection lost, ctx done wraps ctx.Err().
func (t *clientTFakeTransport) Call(ctx context.Context, req *Request, info callInfo) (*Response, error) {
	select {
	case <-t.done:
		return nil, fmt.Errorf("%w: fake transport is down", errUnreachable)
	default:
	}
	r := *req
	id := t.nextID.Add(1)
	r.JSONRPC, r.ID = jsonrpcVersion, intID(id)
	t.record(clientTMsg{call: true, id: id, method: r.Method, params: r.Params, info: info})
	type answer struct {
		resp *Response
		err  error
	}
	ch := make(chan answer, 1)
	go func() {
		resp, err := t.srv.handle(ctx, t, &r, info)
		ch <- answer{resp, err}
	}()
	select {
	case a := <-ch:
		if a.resp != nil {
			out := *a.resp
			out.JSONRPC, out.ID = jsonrpcVersion, r.ID
			return &out, a.err
		}
		return nil, a.err
	case <-ctx.Done():
		return nil, fmt.Errorf("fake call: %w", ctx.Err())
	case <-t.done:
		return nil, fmt.Errorf("%w: fake transport lost", errConnectionLost)
	}
}

func (t *clientTFakeTransport) Notify(ctx context.Context, n *Notification, info callInfo) error {
	select {
	case <-t.done:
		return fmt.Errorf("%w: fake transport is down", errUnreachable)
	default:
	}
	t.record(clientTMsg{method: n.Method, params: n.Params, info: info})
	if t.srv.notify != nil {
		return t.srv.notify(t, n, info)
	}
	return ctx.Err()
}

func (t *clientTFakeTransport) Close(context.Context) error {
	t.closed.Store(true)
	t.lose()
	return nil
}

func (t *clientTFakeTransport) Done() <-chan struct{} { return t.done }

// lose simulates the connection dying (process exit) without Close.
func (t *clientTFakeTransport) lose() {
	t.doneOnce.Do(func() {
		close(t.done)
		t.cancel()
	})
}

// isClosed reports whether the client called Close.
func (t *clientTFakeTransport) isClosed() bool { return t.closed.Load() }

// serverRequest delivers a server→client request and returns the reply
// (nil = no reply), as the transports do through dispatchInbound.
func (t *clientTFakeTransport) serverRequest(method, id string) *Response {
	return t.hooks.onRequest(t.lifeCtx, &Request{JSONRPC: jsonrpcVersion, ID: json.RawMessage(id), Method: method})
}

// serverNotify delivers a server notification.
func (t *clientTFakeTransport) serverNotify(method string, params string) {
	n := &Notification{JSONRPC: jsonrpcVersion, Method: method}
	if params != "" {
		n.Params = json.RawMessage(params)
	}
	t.hooks.onNotification(t.lifeCtx, n)
}

// clientTOK builds a result response.
func clientTOK(v any) *Response {
	var raw json.RawMessage
	switch x := v.(type) {
	case string:
		raw = json.RawMessage(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			panic(err)
		}
		raw = b
	}
	return &Response{Result: raw}
}

// clientTRPCErr builds an error response; data "" means none.
func clientTRPCErr(code int, data string) *Response {
	return &Response{Error: clientTRPC(code, data)}
}

func clientTRPC(code int, data string) *RPCError {
	e := &RPCError{Code: code, Message: fmt.Sprintf("fake error %d", code)}
	if data != "" {
		e.Data = json.RawMessage(data)
	}
	return e
}

// clientTRoutes dispatches by method; unknown methods get -32601.
type clientTRoutes map[string]clientTHandler

func (r clientTRoutes) handler() clientTHandler {
	return func(ctx context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error) {
		if h, ok := r[req.Method]; ok {
			return h(ctx, tr, req, info)
		}
		return clientTRPCErr(codeMethodNotFound, ""), nil
	}
}

// clientTReply returns a handler that always answers resp, err.
func clientTReply(resp *Response, err error) clientTHandler {
	return func(context.Context, *clientTFakeTransport, *Request, callInfo) (*Response, error) {
		return resp, err
	}
}

// clientTHang never answers; it returns when the call is abandoned.
func clientTHang(ctx context.Context, tr *clientTFakeTransport, _ *Request, _ callInfo) (*Response, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-tr.done:
		return nil, errConnectionLost
	}
}

// clientTDelay answers with h after d (or gives up when ctx ends).
func clientTDelay(d time.Duration, h clientTHandler) clientTHandler {
	return func(ctx context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error) {
		select {
		case <-time.After(d):
			return h(ctx, tr, req, info)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// clientTLoseWith simulates the process dying while handling a request.
func clientTLoseWith(err error) clientTHandler {
	return func(_ context.Context, tr *clientTFakeTransport, _ *Request, _ callInfo) (*Response, error) {
		tr.lose()
		return nil, err
	}
}

// clientTStatus builds an HTTP status error, optionally with a JSON-RPC
// error body.
func clientTStatus(status int, rpc *RPCError) error {
	return &httpStatusError{Status: status, RPC: rpc}
}

// clientTSeq answers with the i-th handler on the i-th call (the last
// one repeats).
func clientTSeq(hs ...clientTHandler) clientTHandler {
	var n atomic.Int64
	return func(ctx context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error) {
		i := int(n.Add(1)) - 1
		if i >= len(hs) {
			i = len(hs) - 1
		}
		return hs[i](ctx, tr, req, info)
	}
}

// clientTTool returns a raw tool definition with one string argument.
func clientTTool(name string) string {
	return `{"name":"` + name + `","description":"fake tool ` + name + `","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}}`
}

// clientTToolsPage builds a tools/list result.
func clientTToolsPage(next *string, tools ...string) *Response {
	res := `{"tools":[` + strings.Join(tools, ",") + `]`
	if next != nil {
		b, _ := json.Marshal(*next)
		res += `,"nextCursor":` + string(b)
	}
	return clientTOK(res + "}")
}

// clientTEcho answers tools/call with the tool name and arguments.
func clientTEcho(_ context.Context, _ *clientTFakeTransport, req *Request, _ callInfo) (*Response, error) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return clientTRPCErr(codeInvalidParams, ""), nil
	}
	text, _ := json.Marshal("echo " + p.Name + " " + string(p.Arguments))
	return clientTOK(`{"content":[{"type":"text","text":` + string(text) + `}]}`), nil
}

// clientTHasModernMeta reports whether params carry exactly the modern
// _meta keys.
func clientTHasModernMeta(params json.RawMessage) bool {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	for _, k := range []string{metaProtocolVersion, metaClientInfo, metaClientCapabilities} {
		if _, ok := p.Meta[k]; !ok {
			return false
		}
	}
	return true
}

// clientTInitialize answers initialize with version, or with the offered
// version when version is "".
func clientTInitialize(version string) clientTHandler {
	return func(_ context.Context, _ *clientTFakeTransport, req *Request, _ callInfo) (*Response, error) {
		v := version
		if v == "" {
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return clientTRPCErr(codeInvalidParams, ""), nil
			}
			v = p.ProtocolVersion
		}
		return clientTOK(fmt.Sprintf(`{"protocolVersion":%q,"capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"legacy-fake","version":"1.2.3"},"instructions":"legacy instructions"}`, v)), nil
	}
}

// clientTLegacyGuard rejects, like a dual-era server locked to legacy,
// any request carrying modern _meta or modern framing with -32600.
func clientTLegacyGuard(h clientTHandler) clientTHandler {
	return func(ctx context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error) {
		if strings.Contains(string(req.Params), "io.modelcontextprotocol/") || info.modern {
			return clientTRPCErr(codeInvalidRequest, ""), nil
		}
		return h(ctx, tr, req, info)
	}
}

// clientTLegacyRoutes is a legacy-only server that answers initialize
// with version ("" = whatever was offered).
func clientTLegacyRoutes(version string, tools ...string) clientTRoutes {
	guard := clientTLegacyGuard
	return clientTRoutes{
		methodDiscover:   clientTReply(clientTRPCErr(codeMethodNotFound, ""), nil),
		methodInitialize: guard(clientTInitialize(version)),
		methodPing:       guard(clientTReply(clientTOK(`{}`), nil)),
		methodToolsList:  guard(clientTReply(clientTToolsPage(nil, tools...), nil)),
		methodToolsCall:  guard(clientTEcho),
	}
}

// clientTModernResult is a valid modern DiscoverResult.
const clientTModernResult = `{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{"listChanged":true}},"instructions":"modern instructions","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"modern-fake","version":"9.9"}}}`

// clientTModernGuard rejects requests without the per-request _meta or
// modern framing with -32602.
func clientTModernGuard(h clientTHandler) clientTHandler {
	return func(ctx context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error) {
		if !clientTHasModernMeta(req.Params) || !info.modern || info.protocolVersion != modernProtocolVersion {
			return clientTRPCErr(codeInvalidParams, ""), nil
		}
		return h(ctx, tr, req, info)
	}
}

// clientTModernRoutes is a modern-only server. Requests without the
// per-request _meta get -32602; initialize gets -32022; ping -32601.
func clientTModernRoutes(tools ...string) clientTRoutes {
	guard := clientTModernGuard
	return clientTRoutes{
		methodDiscover:   guard(clientTReply(clientTOK(clientTModernResult), nil)),
		methodInitialize: clientTReply(clientTRPCErr(codeUnsupportedProtocolVersion, `{"supported":["2026-07-28"]}`), nil),
		methodToolsList:  guard(clientTReply(clientTToolsPage(nil, tools...), nil)),
		methodToolsCall:  guard(clientTEcho),
	}
}

// clientTConfig returns a fast test configuration.
func clientTConfig(transport, protocol string) clientConfig {
	return clientConfig{
		name:             "fake",
		transport:        transport,
		protocol:         protocol,
		connectTimeout:   5 * time.Second,
		callTimeout:      5 * time.Second,
		probeTimeout:     100 * time.Millisecond,
		backoffMin:       2 * time.Millisecond,
		backoffMax:       10 * time.Millisecond,
		listRefreshFloor: time.Hour,
	}
}

// clientTNew builds a client on srv (not started). It is closed when the
// test ends.
func clientTNew(t *testing.T, srv *clientTFakeServer, cfg clientConfig) *serverClient {
	t.Helper()
	if cfg.logger == nil {
		cfg.logger = coreTLogger(t)
	}
	c := newServerClient(t.Context(), cfg, srv.factory())
	c.debounce = time.Millisecond
	c.jitter = func(d time.Duration) time.Duration { return d } // exact backoff
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if err := c.close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return c
}

// clientTStart starts c and waits for its first attempt.
func clientTStart(t *testing.T, c *serverClient) {
	t.Helper()
	c.start()
	select {
	case <-c.firstAttempt():
	case <-time.After(5 * time.Second):
		t.Fatal("first connect attempt did not finish")
	}
}

// clientTEventually polls cond until it holds or 5s pass.
func clientTEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	coreTWaitFor(t, 5*time.Second, what, cond)
}

// clientTReadyConn waits until c is ready and returns the connection.
func clientTReadyConn(t *testing.T, c *serverClient) *clientConn {
	t.Helper()
	var conn *clientConn
	clientTEventually(t, "client ready", func() bool {
		cc, err := c.readyConn()
		conn = cc
		return err == nil
	})
	return conn
}

// clientTToolNames returns the names of c.tools().
func clientTToolNames(c *serverClient) []string {
	var out []string
	for _, d := range c.tools() {
		out = append(out, d.Name)
	}
	return out
}
