package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"
)

// Transport is one connection lifetime to one MCP server: one stdio
// process, or one HTTP session (legacy) / endpoint binding (modern).
// Reconnecting means building a new Transport through a transportFactory.
type Transport interface {
	// Call sends req (Method and Params set by the caller) with a fresh
	// transport-assigned integer ID and waits for the matching response.
	// A JSON-RPC error response is returned as (resp, nil); transport
	// failures wrap an errors.go class (errUnreachable, errConnectionLost,
	// errProtocol, errSessionExpired, *httpStatusError) or ctx.Err().
	Call(ctx context.Context, req *Request, info callInfo) (*Response, error)

	// Notify sends n. stdio: returns after the line is written. HTTP:
	// returns after any 2xx.
	Notify(ctx context.Context, n *Notification, info callInfo) error

	// Close tears the connection down, bounded by ctx. Idempotent; safe
	// to call concurrently and after Done has closed.
	Close(ctx context.Context) error

	// Done is closed when the connection is gone. stdio: process exit,
	// stdout EOF/error, fatal framing error, or Close. HTTP: Close only.
	Done() <-chan struct{}
}

// callInfo says how one outbound message is framed. All values are raw;
// transports do the encoding.
type callInfo struct {
	// protocolVersion is the MCP-Protocol-Version header value. "" omits
	// it. HTTP only; stdio ignores it. The HTTP transport also latches the
	// last non-empty value for its own POSTs (cancellations, replies,
	// DELETE).
	protocolVersion string

	// modern selects modern HTTP framing: send Mcp-Method (= req.Method),
	// Mcp-Name and Mcp-Param-*; never send Mcp-Session-Id; cancel by
	// aborting the POST only. HTTP only.
	modern bool

	// name is the raw Mcp-Name value ("" omits). HTTP applies
	// encodeHeaderValue.
	name string

	// paramHeaders maps an x-mcp-header annotation value (e.g. "Region")
	// to the raw argument value rendered as a string. HTTP emits
	// "Mcp-Param-"+key: encodeHeaderValue(value). nil/empty = none.
	paramHeaders map[string]string

	// cancellable: when ctx ends before the response, send
	// notifications/cancelled (stdio, legacy HTTP). false for
	// server/discover and initialize: clients MUST NOT cancel
	// initialize (2025-11-25 cancellation.mdx), and a late discover
	// reply is dropped as an unknown id instead.
	cancellable bool
}

// transportHooks carry what a transport takes from its client: the
// hooks that deliver server-initiated messages (either may be nil) and
// the exit WARN gate the client's transports share.
type transportHooks struct {
	// onRequest runs in its own goroutine per inbound request, at most
	// inboundConcurrency at once per transport (see dispatchInbound). A
	// nil return means "send no reply". Otherwise the transport stamps
	// JSONRPC="2.0" and ID=req.ID (verbatim) and writes/POSTs it with a
	// replyTimeout-bounded ctx derived from the lifetime ctx.
	onRequest func(ctx context.Context, req *Request) *Response

	// onNotification runs synchronously on the reader goroutine. It must
	// not block and must not call the Transport.
	onNotification func(ctx context.Context, n *Notification)

	// inbound bounds this transport's handling of server messages. Each
	// transport installs its own gate in its copy of the hooks when it is
	// built; factories' callers leave it nil. nil is unbounded.
	inbound *inboundGate

	// exitWarned is shared by every transport of one serverClient. A
	// stdio process exit the operator should see logs its stderr tail
	// at WARN only while exitWarned is false, and sets it; later exits
	// log at Debug. The client clears it at a connect it logs at INFO
	// and once a connection has lived stableAfter, so a server that keeps
	// failing at start-up warns once, a connection logged at INFO warns
	// when it crashes, and the quick crashes after it do not. nil warns
	// on every exit. HTTP ignores it.
	exitWarned *atomic.Bool
}

// transportFactory builds a new Transport. ctx bounds construction only;
// the transport derives its lifetime ctx with context.WithoutCancel(ctx)
// plus its own cancel (fired by Close/Done) and passes that lifetime ctx
// to the hooks.
type transportFactory func(ctx context.Context, hooks transportHooks) (Transport, error)

const (
	// cancelNotifyTimeout bounds sending notifications/cancelled, on
	// context.WithTimeout(context.WithoutCancel(callCtx), cancelNotifyTimeout).
	cancelNotifyTimeout = 5 * time.Second

	// replyTimeout bounds writing/POSTing a reply to a server request.
	replyTimeout = 5 * time.Second

	// inboundConcurrency bounds the server requests one transport
	// handles at once, reply included. Thane answers only ping and
	// declines the rest, so a legitimate server never comes close.
	inboundConcurrency = 16

	// maxInboundBatch bounds the elements of one inbound JSON-RPC batch.
	// Servers send batches rarely and short; a longer one is refused so
	// a single line or body cannot start thousands of handlers.
	maxInboundBatch = 128
)

// inboundGate bounds one transport's handling of server-initiated
// messages: the request slots dispatchInbound takes, and whether the
// batch-length WARN was logged. It lives in transportHooks.inbound.
type inboundGate struct {
	slots       chan struct{}
	batchWarned atomic.Bool
}

func newInboundGate() *inboundGate {
	return &inboundGate{slots: make(chan struct{}, inboundConcurrency)}
}

// batchTooLong reports whether n decoded messages exceed maxInboundBatch.
// The caller then skips the whole line or body as a protocol violation;
// the connection stays up. The first refusal per transport logs a WARN,
// later ones Debug. attrs identify the source (pid, method).
func (g *inboundGate) batchTooLong(n int, logger *slog.Logger, attrs ...any) bool {
	if n <= maxInboundBatch {
		return false
	}
	level := slog.LevelDebug
	if g.batchWarned.CompareAndSwap(false, true) {
		level = slog.LevelWarn
	}
	logger.Log(context.Background(), level, "mcp: skipping a JSON-RPC batch from the server that is over the length limit",
		append([]any{"elements", n, "limit", maxInboundBatch}, attrs...)...)
	return true
}

// cancelNotifyContext returns the ctx for sending notifications/cancelled
// after callCtx ended: it keeps callCtx's values (logger) but not its
// cancellation, and is bounded by cancelNotifyTimeout.
func cancelNotifyContext(callCtx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(callCtx), cancelNotifyTimeout)
}

// cancelledParams is the params object of notifications/cancelled.
type cancelledParams struct {
	RequestID json.RawMessage `json:"requestId"`
	Reason    string          `json:"reason,omitempty"`
}

// cancelledNotification builds notifications/cancelled
// {"requestId": id, "reason": reason} (reason omitted when "").
// Transports pass the integer id they assigned, so marshalling cannot
// fail; if it ever did, the notification is sent without params, which
// a server ignores.
func cancelledNotification(id json.RawMessage, reason string) *Notification {
	params, _ := marshalParams(cancelledParams{RequestID: id, Reason: reason})
	return &Notification{JSONRPC: jsonrpcVersion, Method: methodCancelled, Params: params}
}

// dispatchInbound handles one inbound request or notification for both
// transports. Responses are routed by the caller and never passed here.
//
// Requests: hooks.onRequest(lifeCtx, req) runs in its own goroutine,
// holding one of hooks.inbound's inboundConcurrency slots until its
// reply is sent; a non-nil reply is stamped with JSONRPC and the
// request's ID (verbatim) and handed to reply with a replyTimeout ctx
// derived from lifeCtx.
// Reply errors are logged at Debug. A request is dropped (Debug) when
// there is no onRequest hook, lifeCtx is already done, or every slot is
// taken. Dropping never blocks the reader, and a server that gets no
// answer to a ping retries or gives up on its own.
//
// Notifications: hooks.onNotification(lifeCtx, n) runs synchronously.
//
// Anything else (an invalid message, or a response handed in by
// mistake) is logged at Debug and dropped.
func dispatchInbound(lifeCtx context.Context, hooks transportHooks, m *wireMessage, reply func(ctx context.Context, resp *Response) error, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	switch kind := m.kind(); kind {
	case kindRequest:
		req := m.request()
		if hooks.onRequest == nil || lifeCtx.Err() != nil {
			logger.Debug("mcp: dropping server request", "method", req.Method)
			return
		}
		gate := hooks.inbound
		if gate == nil {
			go answerInbound(lifeCtx, hooks.onRequest, req, reply, logger)
			return
		}
		select {
		case gate.slots <- struct{}{}:
		default:
			logger.Debug("mcp: dropping server request; too many in progress", "method", req.Method, "limit", inboundConcurrency)
			return
		}
		go func() {
			defer func() { <-gate.slots }()
			answerInbound(lifeCtx, hooks.onRequest, req, reply, logger)
		}()
	case kindNotification:
		if hooks.onNotification != nil {
			hooks.onNotification(lifeCtx, m.notification())
		}
	default:
		logger.Debug("mcp: dropping inbound message", "kind", kind.String())
	}
}

// answerInbound runs one onRequest hook and sends its reply, if any.
func answerInbound(lifeCtx context.Context, onRequest func(context.Context, *Request) *Response, req *Request, reply func(context.Context, *Response) error, logger *slog.Logger) {
	resp := onRequest(lifeCtx, req)
	if resp == nil || reply == nil {
		return
	}
	out := *resp
	out.JSONRPC = jsonrpcVersion
	out.ID = req.ID
	if out.Result == nil && out.Error == nil {
		out.Result = json.RawMessage("{}") // a reply always carries a result or an error
	}
	ctx, cancel := context.WithTimeout(lifeCtx, replyTimeout)
	defer cancel()
	if err := reply(ctx, &out); err != nil {
		logger.Debug("mcp: reply to server request failed", "method", req.Method, "error", err)
	}
}
