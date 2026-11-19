package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Header names the Streamable HTTP transport frames. Go canonicalises
// names, and servers compare them case-insensitively.
const (
	httpHeaderAccept          = "Accept"
	httpHeaderContentType     = "Content-Type"
	httpHeaderProtocolVersion = "MCP-Protocol-Version"
	httpHeaderSessionID       = "Mcp-Session-Id"
	httpHeaderMethod          = "Mcp-Method"
	httpHeaderName            = "Mcp-Name"
	httpHeaderParamPrefix     = "Mcp-Param-"
	httpHeaderLastEventID     = "Last-Event-ID"
	httpHeaderWWWAuthenticate = "WWW-Authenticate"
	httpHeaderLocation        = "Location"

	httpAcceptValue = "application/json, text/event-stream"
	httpContentJSON = "application/json"
	httpContentSSE  = "text/event-stream"
)

const (
	// httpErrorBodyLimit bounds how much of a non-2xx body is read to
	// look for a JSON-RPC error.
	httpErrorBodyLimit = 64 << 10
	// httpLogBodyCap bounds httpStatusError.Body and WWWAuthenticate.
	httpLogBodyCap = 1 << 10
	// httpDrainLimit bounds draining a finished body for connection reuse.
	httpDrainLimit = 4096
	// httpSSETailWait is how long an SSE response stream may take to end
	// after the matching response before it is aborted. Ending it lets
	// net/http reuse the connection; the wait is short so a stream the
	// server keeps open never holds the call.
	httpSSETailWait = 50 * time.Millisecond
	// httpDeleteTimeout caps the session DELETE sent by Close.
	httpDeleteTimeout = 5 * time.Second
)

// httpConfig configures one Streamable HTTP endpoint.
type httpConfig struct {
	url     string
	headers map[string]string // operator headers; reserved names already removed by the Manager
	logger  *slog.Logger
	client  *http.Client // nil → newHTTPClient(); tests inject httptest clients
}

// httpHeader is one validated operator header.
type httpHeader struct {
	name, value string
}

// httpTransport implements Transport for one Streamable HTTP session
// (legacy) or endpoint binding (modern). Every JSON-RPC message is its
// own POST; the response comes back as a JSON body or an SSE stream.
// Done closes only on Close: HTTP has no connection whose loss the
// transport could observe, and a legacy session expiry is reported per
// request as errSessionExpired instead.
type httpTransport struct {
	url     string
	headers []httpHeader // validated, sorted; never a name the transport frames
	client  *http.Client
	logger  *slog.Logger
	hooks   transportHooks // hooks.inbound is this transport's own gate

	// maxMessage bounds one JSON body or SSE event (maxMessageBytes;
	// tests lower it).
	maxMessage int

	lifeCtx    context.Context // passed to hooks; cancelled by Close
	lifeCancel context.CancelFunc
	nextID     atomic.Int64

	mu      sync.Mutex
	session string // legacy Mcp-Session-Id from the initialize response
	version string // latched MCP-Protocol-Version for transport-originated POSTs
	// expired: the server rejected our session (404, or a 400 with an
	// unattributed JSON-RPC error; see httpSessionRejected). Close then
	// skips the DELETE.
	expired  bool
	retiring bool // set by retire: no new requests, in-flight ones finish
	closing  bool // set by Close

	inflight sync.WaitGroup // exchanges admitted by begin; retire waits for them

	closeOnce sync.Once
	done      chan struct{}
}

var _ Transport = (*httpTransport)(nil)

// httpFrame says which protocol headers one HTTP request carries.
type httpFrame struct {
	version string // MCP-Protocol-Version; "" omits
	session bool   // send Mcp-Session-Id when a session exists
	modern  bool   // add Mcp-Method/Mcp-Name/Mcp-Param-*
	method  string // Mcp-Method value (modern only)
	name    string // raw Mcp-Name (modern only)
	params  map[string]string
}

// newHTTPTransport validates cfg and builds the transport. It does no
// I/O. Operator headers with an invalid name or value are dropped with
// a WARN (naming the header, never its value), and so are names the
// transport frames itself, so protocol headers always win.
func newHTTPTransport(ctx context.Context, cfg httpConfig, hooks transportHooks) (*httpTransport, error) {
	logger := cfg.logger
	if logger == nil {
		logger = slog.Default()
	}
	u, err := url.Parse(cfg.url)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: the MCP server url must be an absolute http or https URL", errUnreachable)
	}
	client := cfg.client
	if client == nil {
		client = newHTTPClient()
	}
	lifeCtx, lifeCancel := context.WithCancel(context.WithoutCancel(ctx))
	hooks.inbound = newInboundGate()
	return &httpTransport{
		url:        cfg.url,
		headers:    httpOperatorHeaders(cfg.headers, logger),
		client:     client,
		logger:     logger,
		hooks:      hooks,
		maxMessage: maxMessageBytes,
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
		done:       make(chan struct{}),
	}, nil
}

// httpOperatorHeaders validates and sorts the configured headers.
func httpOperatorHeaders(in map[string]string, logger *slog.Logger) []httpHeader {
	out := make([]httpHeader, 0, len(in))
	for name, value := range in {
		switch {
		case !isHeaderToken(name):
			logger.Warn("mcp: dropping configured HTTP header with an invalid name", "header", truncateUTF8(name, 64))
		case !httpHeaderValueValid(value):
			logger.Warn("mcp: dropping configured HTTP header with an invalid value", "header", name)
		case httpFramedHeader(name):
			logger.Warn("mcp: dropping configured HTTP header the MCP transport sets itself", "header", name)
		default:
			out = append(out, httpHeader{name: http.CanonicalHeaderKey(name), value: value})
		}
	}
	slices.SortFunc(out, func(a, b httpHeader) int { return strings.Compare(a.name, b.name) })
	return out
}

// httpFramedHeader reports header names the transport owns.
func httpFramedHeader(name string) bool {
	c := http.CanonicalHeaderKey(name)
	switch c {
	case httpHeaderAccept, httpHeaderContentType, http.CanonicalHeaderKey(httpHeaderProtocolVersion),
		httpHeaderSessionID, httpHeaderMethod, httpHeaderName, http.CanonicalHeaderKey(httpHeaderLastEventID):
		return true
	}
	return strings.HasPrefix(c, httpHeaderParamPrefix)
}

// httpHeaderValueValid rejects values net/http would refuse to send:
// control characters other than horizontal tab (CR and LF included).
func httpHeaderValueValid(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; (c < 0x20 && c != '\t') || c == 0x7F {
			return false
		}
	}
	return true
}

// httpVisibleASCII reports a non-empty string of 0x21-0x7E only (the
// Mcp-Session-Id alphabet).
func httpVisibleASCII(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x21 || v[i] > 0x7E {
			return false
		}
	}
	return true
}

// Call POSTs req with a fresh integer ID and returns the matching
// JSON-RPC response from the JSON body or SSE stream (streamable-http
// "Sending Messages"). On a legacy cancellable call whose ctx ends
// first, notifications/cancelled is POSTed before returning; modern
// calls cancel only by aborting the POST.
func (t *httpTransport) Call(ctx context.Context, req *Request, info callInfo) (*Response, error) {
	if err := t.closedErr(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	out := *req
	out.JSONRPC = jsonrpcVersion
	id := t.nextID.Add(1)
	out.ID = intID(id)
	body, err := json.Marshal(&out)
	if err != nil {
		return nil, fmt.Errorf("%w: http: marshal request: %w", errProtocol, err)
	}
	isInit := req.Method == methodInitialize
	t.latch(info.protocolVersion, isInit)
	resp, err := t.exchange(ctx, req.Method, body, t.frame(req.Method, info, isInit), id, isInit)
	if err != nil && info.cancellable && !info.modern && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		t.sendCancel(ctx, out.ID, info)
	}
	return resp, err
}

// Notify POSTs n; any 2xx (202 expected) is success.
func (t *httpTransport) Notify(ctx context.Context, n *Notification, info callInfo) error {
	if t.isClosing() {
		return fmt.Errorf("%w: http: transport closed", errUnreachable)
	}
	out := *n
	out.JSONRPC = jsonrpcVersion
	body, err := json.Marshal(&out)
	if err != nil {
		return fmt.Errorf("%w: http: marshal notification: %w", errProtocol, err)
	}
	t.latch(info.protocolVersion, false)
	return t.send(ctx, body, t.frame(n.Method, info, false))
}

// Close ends the transport: in-flight POSTs are aborted (retire first
// lets them finish), a legacy session is ended with DELETE (bounded by
// ctx and httpDeleteTimeout; any status, 404 and 405 included, is
// fine), idle connections are closed and Done closes. Idempotent and
// safe to call concurrently.
func (t *httpTransport) Close(ctx context.Context) error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closing = true
		deleteSession := t.session != "" && !t.expired
		version := t.version
		t.mu.Unlock()
		t.lifeCancel()
		if deleteSession {
			t.deleteSession(ctx, version)
		}
		t.client.CloseIdleConnections()
		close(t.done)
	})
	return nil
}

// Done is closed once Close has run.
func (t *httpTransport) Done() <-chan struct{} { return t.done }

// retire stops admitting requests and waits, bounded by ctx, for the
// exchanges already in flight to finish on their own; their replies to
// server requests and their cancellations are still sent. The client
// retires a transport whose session expired instead of closing it, so
// sibling requests still get their real answers (the server rejects
// them unprocessed, and the client retries them on a new session). It
// does not close the transport: Close must still be called.
func (t *httpTransport) retire(ctx context.Context) {
	t.mu.Lock()
	t.retiring = true
	t.mu.Unlock()
	idle := make(chan struct{})
	go func() {
		t.inflight.Wait()
		close(idle)
	}()
	select {
	case <-idle:
	case <-ctx.Done():
	}
}

// begin admits one exchange, or refuses it once the transport is
// retiring or closed. An admitted exchange must call t.inflight.Done.
func (t *httpTransport) begin() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.retiring || t.closing {
		return t.closedErrLocked()
	}
	t.inflight.Add(1)
	return nil
}

func (t *httpTransport) isClosing() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closing
}

// closedErr is the error for a request on a retiring or closed
// transport, or nil while it is open.
func (t *httpTransport) closedErr() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.retiring && !t.closing {
		return nil
	}
	return t.closedErrLocked()
}

// closedErrLocked: a request refused because its session expired was
// never processed, so it reports errSessionExpired and the client
// retries it on the replacement connection. t.mu must be held.
func (t *httpTransport) closedErrLocked() error {
	if t.expired {
		return fmt.Errorf("%w: http: session expired", errSessionExpired)
	}
	return fmt.Errorf("%w: http: transport closed", errUnreachable)
}

// latch records the protocol version for transport-originated POSTs
// (DELETE). initialize resets the latch and the session: the handshake
// never carries MCP-Protocol-Version or Mcp-Session-Id, which 2025-11-25
// defines only for requests after initialize, and a discover probe's
// modern version must not leak onto a legacy session.
func (t *httpTransport) latch(version string, isInit bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case isInit:
		t.version, t.session, t.expired = "", "", false
	case version != "":
		t.version = version
	}
}

// frame builds the header frame for a client request or notification.
func (t *httpTransport) frame(method string, info callInfo, isInit bool) httpFrame {
	f := httpFrame{
		version: info.protocolVersion,
		session: !info.modern && !isInit,
		modern:  info.modern,
		method:  method,
		name:    info.name,
		params:  info.paramHeaders,
	}
	if isInit {
		f.version = ""
	}
	return f
}

// newRequest builds one HTTP request: operator headers first, then the
// protocol headers (which win). It reports whether Mcp-Session-Id was
// attached, because only then can a 404 (or a 400 with an unattributed
// JSON-RPC error) mean the session expired.
func (t *httpTransport) newRequest(ctx context.Context, method string, body []byte, f httpFrame) (*http.Request, bool, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.url, rd)
	if err != nil {
		return nil, false, fmt.Errorf("%w: build http request: %w", errUnreachable, err)
	}
	h := req.Header
	for _, kv := range t.headers {
		h.Set(kv.name, kv.value)
	}
	if body != nil {
		h.Set(httpHeaderContentType, httpContentJSON)
	}
	h.Set(httpHeaderAccept, httpAcceptValue)
	if f.version != "" {
		h.Set(httpHeaderProtocolVersion, f.version)
	}
	if f.modern {
		t.setModernHeaders(h, f)
	}
	sentSession := false
	if f.session && !f.modern {
		t.mu.Lock()
		sid := t.session
		t.mu.Unlock()
		if sid != "" {
			h.Set(httpHeaderSessionID, sid)
			sentSession = true
		}
	}
	return req, sentSession, nil
}

// setModernHeaders adds Mcp-Method, Mcp-Name and Mcp-Param-* (values
// encoded with encodeHeaderValue). A parameter header whose name is not
// an RFC 9110 token is skipped with a WARN; the Client validates
// x-mcp-header annotations, so this is defence in depth.
func (t *httpTransport) setModernHeaders(h http.Header, f httpFrame) {
	if f.method != "" {
		h.Set(httpHeaderMethod, f.method)
	}
	if f.name != "" {
		h.Set(httpHeaderName, encodeHeaderValue(f.name))
	}
	for key, value := range f.params {
		if !isHeaderToken(key) {
			t.logger.Warn("mcp: skipping Mcp-Param header with an invalid name", "param", truncateUTF8(key, 64))
			continue
		}
		h.Set(httpHeaderParamPrefix+key, encodeHeaderValue(value))
	}
}

// sendCancel POSTs notifications/cancelled for a legacy call whose ctx
// ended, on a ctx that keeps callCtx's values but not its cancellation.
func (t *httpTransport) sendCancel(callCtx context.Context, id json.RawMessage, info callInfo) {
	reason := "cancelled"
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		reason = "timeout"
	}
	ctx, cancel := cancelNotifyContext(callCtx)
	defer cancel()
	n := cancelledNotification(id, reason)
	body, err := json.Marshal(n)
	if err == nil {
		err = t.send(ctx, body, httpFrame{version: info.protocolVersion, session: true})
	}
	if err != nil {
		t.logger.Debug("mcp: sending notifications/cancelled failed", "error", err)
	}
}

// replier returns the reply func dispatchInbound uses for a server
// request: the JSON-RPC response is POSTed with frame rf (the protocol
// version of the POST the request arrived on and, for legacy, the
// session).
func (t *httpTransport) replier(rf httpFrame) func(ctx context.Context, resp *Response) error {
	return func(ctx context.Context, resp *Response) error {
		body, err := json.Marshal(resp)
		if err != nil {
			return fmt.Errorf("%w: marshal reply: %w", errProtocol, err)
		}
		return t.send(ctx, body, rf)
	}
}

// deleteSession ends the legacy session. Failures only log: the server
// expires the session on its own.
func (t *httpTransport) deleteSession(ctx context.Context, version string) {
	ctx, cancel := context.WithTimeout(ctx, httpDeleteTimeout)
	defer cancel()
	req, _, err := t.newRequest(ctx, http.MethodDelete, nil, httpFrame{version: version, session: true})
	if err != nil {
		t.logger.Debug("mcp: ending HTTP session failed", "error", err)
		return
	}
	resp, err := t.client.Do(req)
	if err != nil {
		t.logger.Debug("mcp: ending HTTP session failed", "error", httpRedact(err))
		return
	}
	httpDrain(resp)
	t.logger.Debug("mcp: ended HTTP session", "status", resp.StatusCode)
}
