package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
)

// serverLogCapBytes bounds server-authored log text (notifications/message
// data, progress params) copied into Thane's logs.
const serverLogCapBytes = 2048

// hooksFor binds the inbound hooks of one transport to conn, so the
// replies follow that connection's era (AM-33).
func (c *serverClient) hooksFor(conn *clientConn) transportHooks {
	return transportHooks{
		onRequest: func(ctx context.Context, req *Request) *Response {
			return c.handleRequest(ctx, conn, req)
		},
		onNotification: func(ctx context.Context, n *Notification) {
			c.handleNotification(ctx, conn, n)
		},
	}
}

// handleRequest answers a server-to-client request (critic A13).
//
// Legacy, or era not yet known (during the probe): every request gets a
// reply. ping → {}; elicitation/create → -32602 (Thane declares no
// elicitation modes); roots/list, sampling/createMessage and anything
// else → -32601 (Thane declares none of those capabilities).
//
// Modern: servers must not send requests and clients must not write
// responses, so the request is logged as a protocol violation (WARN once
// per method) and never answered.
func (c *serverClient) handleRequest(_ context.Context, conn *clientConn, req *Request) *Response {
	if conn.currentEra() == eraModern {
		c.warnOnce("modern-request:"+logText(req.Method),
			"mcp: the server sent a request, which protocol "+modernProtocolVersion+" forbids; not replying",
			"method", logText(req.Method))
		return nil
	}
	switch req.Method {
	case methodPing:
		return &Response{Result: json.RawMessage(`{}`)}
	case methodElicitation:
		c.logger.Debug("mcp: declined a server elicitation request")
		return &Response{Error: &RPCError{Code: codeInvalidParams, Message: "elicitation is not supported by this client"}}
	default:
		c.logger.Debug("mcp: declined a server request", "method", logText(req.Method))
		return &Response{Error: &RPCError{Code: codeMethodNotFound, Message: "Method not found"}}
	}
}

// handleNotification handles a server notification. It runs on the
// transport's reader goroutine, so it never blocks and never calls the
// transport.
func (c *serverClient) handleNotification(ctx context.Context, _ *clientConn, n *Notification) {
	switch n.Method {
	case methodToolsListChanged:
		c.logger.Debug("mcp: server reported a changed tool list")
		c.postRefresh()
	case methodProgress:
		c.logger.Debug("mcp: server progress", "params", serverLogText(n.Params))
	case methodLogMessage:
		c.logServerMessage(ctx, n.Params)
	case methodCancelled:
		// Thane never has a server request in flight long enough to
		// cancel; nothing to do.
	default:
		c.logger.Debug("mcp: ignoring a server notification", "method", logText(n.Method))
	}
}

// logServerMessage copies a notifications/message into Thane's log. The
// text is server-authored: it goes to the operator log only, never to
// the model, and never above WARN.
func (c *serverClient) logServerMessage(ctx context.Context, params json.RawMessage) {
	var msg struct {
		Level  string          `json:"level"`
		Logger string          `json:"logger"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(params, &msg); err != nil {
		c.logger.Debug("mcp: malformed server log message", "error", err)
		return
	}
	c.logger.Log(ctx, serverLogLevel(msg.Level), "mcp: server log message",
		"level", logText(msg.Level),
		"logger", logText(msg.Logger),
		"data", serverLogText(msg.Data))
}

// serverLogLevel maps an MCP (RFC 5424) log level to slog: debug, info
// and notice → Debug; warning → Info; error and above → Warn. Unknown
// levels are Debug.
func serverLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "warning":
		return slog.LevelInfo
	case "error", "critical", "alert", "emergency":
		return slog.LevelWarn
	default:
		return slog.LevelDebug
	}
}

// serverLogText renders server-supplied JSON for a log attribute: a JSON
// string unquoted, anything else compacted; capped.
func serverLogText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return truncateUTF8(s, serverLogCapBytes)
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return truncateUTF8(string(raw), serverLogCapBytes)
	}
	return truncateUTF8(buf.String(), serverLogCapBytes)
}
