package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
)

// exchange POSTs one request body and reads its response. The
// POST runs on its own ctx, cancelled when the caller's ctx ends, when
// the transport closes, or once the matching response has arrived. It
// counts as in flight for retire until it returns.
func (t *httpTransport) exchange(ctx context.Context, method string, body []byte, f httpFrame, id int64, isInit bool) (*Response, error) {
	if err := t.begin(method); err != nil {
		return nil, err
	}
	defer t.inflight.Done()
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(t.lifeCtx, cancel)
	defer stop()

	hreq, sentSession, err := t.newRequest(reqCtx, http.MethodPost, body, f)
	if err != nil {
		return nil, err
	}
	hresp, err := t.client.Do(hreq)
	if err != nil {
		t.logRefusedRedirect(hresp, err)
		return nil, httpRoundTripError(ctx, t.lifeDone(), method, err)
	}
	if !httpSuccess(hresp.StatusCode) {
		return nil, t.statusError(hresp, sentSession)
	}
	if isInit {
		t.captureSession(hresp)
	}
	if hresp.StatusCode == http.StatusAccepted || hresp.StatusCode == http.StatusNoContent {
		httpDrain(hresp)
		return nil, fmt.Errorf("%w: http %s: the server answered a request with status %d and no response", errProtocol, method, hresp.StatusCode)
	}
	// Replies to server requests on this response are framed like the
	// POST itself; on a legacy session they carry the session id (for
	// initialize, the one this very response just assigned).
	rf := httpFrame{version: f.version, session: !f.modern}
	switch ct := httpMediaType(hresp.Header.Get(httpHeaderContentType)); ct {
	case httpContentJSON:
		return t.readJSON(ctx, hresp, method, id, rf)
	case httpContentSSE:
		return t.readSSE(ctx, hresp, cancel, method, id, rf)
	default:
		httpDrain(hresp)
		return nil, fmt.Errorf("%w: http %s: unexpected response content type %q", errProtocol, method, truncateUTF8(ct, 64))
	}
}

// send POSTs a notification, cancellation or reply. Any 2xx is
// success; the body is drained (JSON or empty) or the stream aborted
// (SSE). Errors follow the same classes as Call.
func (t *httpTransport) send(ctx context.Context, what string, body []byte, f httpFrame) error {
	if t.isClosing() {
		return fmt.Errorf("%w: http %s: transport closed", errUnreachable, what)
	}
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(t.lifeCtx, cancel)
	defer stop()

	hreq, sentSession, err := t.newRequest(reqCtx, http.MethodPost, body, f)
	if err != nil {
		return err
	}
	hresp, err := t.client.Do(hreq)
	if err != nil {
		t.logRefusedRedirect(hresp, err)
		return httpRoundTripError(ctx, t.lifeDone(), what, err)
	}
	if !httpSuccess(hresp.StatusCode) {
		return t.statusError(hresp, sentSession)
	}
	if httpMediaType(hresp.Header.Get(httpHeaderContentType)) == httpContentSSE {
		cancel()
		_ = hresp.Body.Close()
		return nil
	}
	httpDrain(hresp)
	return nil
}

// readJSON reads a JSON body (one message or a batch), dispatches any
// server requests and notifications, and returns the response that
// answers id.
func (t *httpTransport) readJSON(ctx context.Context, hresp *http.Response, method string, id int64, rf httpFrame) (*Response, error) {
	defer httpDrain(hresp)
	b, err := io.ReadAll(io.LimitReader(hresp.Body, int64(t.maxMessage)+1))
	if err != nil {
		return nil, httpRoundTripError(ctx, t.lifeDone(), method, err)
	}
	if len(b) > t.maxMessage {
		return nil, fmt.Errorf("%w: http %s: response body exceeds %d bytes", errProtocol, method, t.maxMessage)
	}
	msgs, err := decodeMessages(b)
	if err != nil {
		return nil, fmt.Errorf("%w: http %s: malformed JSON response: %w", errProtocol, method, err)
	}
	responses := 0
	for i := range msgs {
		if msgs[i].kind() == kindResponse {
			responses++
		}
	}
	var match *Response
	for i := range msgs {
		m := &msgs[i]
		if m.kind() != kindResponse {
			t.dispatch(m, rf)
			continue
		}
		if match == nil && (responses == 1 || httpAnswers(m, id)) {
			match = m.response()
			continue
		}
		t.logger.Debug("mcp: dropping unmatched response", "method", method)
	}
	if match == nil {
		return nil, fmt.Errorf("%w: http %s: the response body carried no response to the request", errProtocol, method)
	}
	return match, nil
}

// readSSE reads an SSE response stream until the response that answers
// id arrives, dispatching server requests and notifications on the way.
// Each event's data is matched like a JSON body (readJSON): the only
// response in it answers the request even when its id differs, because
// the stream belongs to this one request. Once the response is in, the
// stream gets httpSSETailWait to end, which keeps the HTTP/1.1
// connection reusable for a server that ends it promptly (the spec says
// it SHOULD), and is then aborted, never drained, so a server that keeps
// the stream open cannot hold the call.
func (t *httpTransport) readSSE(ctx context.Context, hresp *http.Response, cancel context.CancelFunc, method string, id int64, rf httpFrame) (*Response, error) {
	defer func() {
		cancel()
		_ = hresp.Body.Close()
	}()
	r := newSSEReader(hresp.Body, t.maxMessage)
	for {
		ev, err := r.next()
		if err != nil {
			if errors.Is(err, errProtocol) {
				return nil, fmt.Errorf("mcp: http %s: %w", method, err)
			}
			if errors.Is(err, io.EOF) && ctx.Err() == nil && !t.lifeDone() {
				return nil, fmt.Errorf("%w: http %s: the response stream ended before the response", errConnectionLost, method)
			}
			return nil, httpRoundTripError(ctx, t.lifeDone(), method, err)
		}
		if ev.event != sseDefaultEvent {
			t.logger.Debug("mcp: ignoring SSE event", "event", truncateUTF8(ev.event, 64))
			continue
		}
		if len(bytes.TrimSpace(ev.data)) == 0 {
			continue // priming or keep-alive event
		}
		msgs, err := decodeMessages(ev.data)
		if err != nil {
			return nil, fmt.Errorf("%w: http %s: malformed SSE message: %w", errProtocol, method, err)
		}
		if match := t.matchSSE(msgs, method, id, rf); match != nil {
			httpSSETail(hresp.Body, cancel)
			return match, nil
		}
	}
}

// matchSSE dispatches the requests and notifications of one SSE event
// and returns the response that answers id, if the event carries one: the
// response with that id, else the event's only response. A lone response
// with another id is accepted the way readJSON accepts it; the server
// sends nothing else on this request's stream, and Thane never batches.
func (t *httpTransport) matchSSE(msgs []wireMessage, method string, id int64, rf httpFrame) *Response {
	responses := 0
	for i := range msgs {
		if msgs[i].kind() == kindResponse {
			responses++
		}
	}
	var match *Response
	for i := range msgs {
		m := &msgs[i]
		switch {
		case m.kind() != kindResponse:
			t.dispatch(m, rf)
		case match == nil && httpAnswers(m, id):
			match = m.response()
		case match == nil && responses == 1:
			t.logger.Debug("mcp: accepting the only response on the request's stream although its id does not match", "method", method)
			match = m.response()
		default:
			t.logger.Debug("mcp: dropping unmatched response", "method", method)
		}
	}
	return match
}

// httpSSETail gives a response stream httpSSETailWait to end after the
// matching event, reading at most httpDrainLimit more bytes, then lets
// the caller abort it. A stream that reached EOF has already returned its
// connection to the pool, so the later abort is harmless. The bytes are
// discarded, so reading underneath the SSE parser's buffer is fine.
func httpSSETail(body io.Reader, abort context.CancelFunc) {
	timer := time.AfterFunc(httpSSETailWait, abort)
	defer timer.Stop()
	_, _ = io.Copy(io.Discard, io.LimitReader(body, httpDrainLimit))
}

// dispatch hands an inbound request or notification to the hooks.
// Replies to requests are POSTed with the reply frame rf.
func (t *httpTransport) dispatch(m *wireMessage, rf httpFrame) {
	dispatchInbound(t.lifeCtx, t.hooks, m, t.replier(rf), t.logger)
}

// httpAnswers reports whether m answers request id: its id is that
// integer, or it is an error with an absent or null id (A14: the POST,
// not the id, correlates an error the server could not attribute).
func httpAnswers(m *wireMessage, id int64) bool {
	if got, ok := idInt(m.ID); ok {
		return got == id
	}
	return !hasID(m.ID) && m.Error != nil
}

// statusError turns a non-2xx response into *httpStatusError, reading
// at most httpErrorBodyLimit bytes to find a JSON-RPC error (any id; the
// POST correlates it). When a request that carried Mcp-Session-Id is
// rejected as httpSessionRejected describes, the server no longer knows
// the session: errSessionExpired wraps the status error.
func (t *httpTransport) statusError(hresp *http.Response, sentSession bool) error {
	text := httpkit.ReadErrorBody(hresp.Body, httpErrorBodyLimit)
	rpcErr, attributed := httpRPCError(text)
	statusErr := &httpStatusError{
		Status:          hresp.StatusCode,
		RPC:             rpcErr,
		WWWAuthenticate: truncateUTF8(hresp.Header.Get(httpHeaderWWWAuthenticate), httpLogBodyCap),
		Body:            truncateUTF8(text, httpLogBodyCap),
	}
	if sentSession && httpSessionRejected(hresp.StatusCode, rpcErr, attributed) {
		t.mu.Lock()
		t.expired = true
		t.mu.Unlock()
		return fmt.Errorf("%w: %w", errSessionExpired, statusErr)
	}
	return statusErr
}

// httpSessionRejected reports a status that rejects a request's session
// unprocessed: 404 (the spec's signal), or a 400 whose body is one
// JSON-RPC error with no id. The second is how the TypeScript SDK's
// server examples, which many servers copy, answer an Mcp-Session-Id
// they no longer know ("Bad Request: No valid session ID provided"); an
// error the server could not attribute to the request means it never
// processed it, so re-initializing and retrying once is safe.
func httpSessionRejected(status int, rpcErr *RPCError, attributed bool) bool {
	switch status {
	case http.StatusNotFound:
		return true
	case http.StatusBadRequest:
		return rpcErr != nil && !attributed
	default:
		return false
	}
}

// httpRPCError returns the error object of body when body is exactly
// one JSON-RPC response carrying an error, else nil. attributed reports
// whether that response carries an id.
func httpRPCError(body string) (rpcErr *RPCError, attributed bool) {
	msgs, err := decodeMessages([]byte(body))
	if err != nil || len(msgs) != 1 || msgs[0].kind() != kindResponse {
		return nil, false
	}
	return msgs[0].Error, hasID(msgs[0].ID)
}

// captureSession records Mcp-Session-Id from a 2xx initialize response
// (AM-25). A value outside visible ASCII is ignored with a WARN. The
// value is never logged.
func (t *httpTransport) captureSession(hresp *http.Response) {
	sid := hresp.Header.Get(httpHeaderSessionID)
	if sid == "" {
		return
	}
	if !httpVisibleASCII(sid) {
		t.logger.Warn("mcp: ignoring an Mcp-Session-Id with characters outside visible ASCII", "length", len(sid))
		return
	}
	t.mu.Lock()
	t.session = sid
	t.mu.Unlock()
	t.logger.Debug("mcp: HTTP session established")
}

// logRefusedRedirect tells the operator where a refused redirect
// pointed, so they can configure the final URL. The query string is
// left out because it may carry credentials. http.Client returns a
// non-nil response with an error only when CheckRedirect refused a hop.
func (t *httpTransport) logRefusedRedirect(hresp *http.Response, err error) {
	if hresp == nil || !errors.Is(err, errProtocol) {
		return
	}
	loc := httpRedactURL(hresp.Header.Get(httpHeaderLocation))
	t.logger.Warn("mcp: refused HTTP redirect; set url to the final MCP endpoint (only 307/308 to the same host are followed)",
		"status", hresp.StatusCode, "location", truncateUTF8(loc, 512))
}

func (t *httpTransport) lifeDone() bool { return t.lifeCtx.Err() != nil }

// httpDrain drains a finished body for connection reuse and closes it.
func httpDrain(hresp *http.Response) {
	httpkit.DrainAndClose(hresp.Body, httpDrainLimit)
}

func httpSuccess(code int) bool { return code >= 200 && code <= 299 }

// httpMediaType returns the lower-cased media type of a Content-Type
// value without parameters.
func httpMediaType(v string) string {
	if mt, _, err := mime.ParseMediaType(v); err == nil {
		return mt
	}
	mt, _, _ := strings.Cut(v, ";")
	return strings.ToLower(strings.TrimSpace(mt))
}
