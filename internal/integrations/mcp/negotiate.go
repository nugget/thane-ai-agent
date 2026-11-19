package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// maxNegotiationSteps bounds the discover/initialize exchanges of one
// connect attempt. The per-kind retry flags in negotiate already bound
// it; this is a backstop.
const maxNegotiationSteps = 6

// negotiateStep is the next negotiation request to send.
type negotiateStep struct {
	method  string // methodDiscover or methodInitialize
	version string // version to offer
}

// negotiate builds a transport and settles the era (2026-07-28
// versioning.mdx "Backward Compatibility with Initialization-Based
// Versions"), driving the classifiers classifyDiscover and
// classifyInitialize. It returns the connection in use even on failure
// (it may differ from the first one after a stdio respawn) so the
// caller can close it.
//
// Retry bounds per attempt: one discover re-probe after a -32022, one
// initialize re-offer of a version an error named, and one respawn of a
// stdio server that died during the probe. Neither discover nor
// initialize is ever cancellable: the spec forbids cancelling
// initialize, and a late discover reply finds no pending id and is
// dropped. After a successful initialize, notifications/initialized is
// sent before anything else (2025-11-25 lifecycle.mdx "Initialization").
func (c *serverClient) negotiate(ctx context.Context) (*clientConn, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	mode := c.cfg.protocol
	step := negotiateStep{method: methodDiscover, version: modernProtocolVersion}
	cachedLegacy := mode == protocolAuto && c.cachedEra() == eraLegacy
	if mode == protocolLegacy || cachedLegacy {
		step = negotiateStep{method: methodInitialize, version: handshakeOfferVersion}
	}
	var reprobed, reoffered, respawned bool
	for range maxNegotiationSteps {
		var (
			v     eraVerdict
			cause error
		)
		if step.method == methodDiscover {
			resp, err := c.sendDiscover(ctx, conn, step.version)
			if ctx.Err() != nil {
				// The whole attempt ran out, not just the probe.
				return conn, fmt.Errorf("mcp: %s: %w", methodDiscover, ctx.Err())
			}
			v, cause = classifyDiscover(c.cfg.transport, resp, err), err
		} else {
			resp, err := c.sendInitialize(ctx, conn, step.version)
			v, cause = classifyInitialize(resp, err), err
			if cachedLegacy && v.step == verdictFail && serverAnswered(resp, err) {
				// The cached era no longer works: probe next time.
				c.rememberEra(eraUnknown)
			}
		}
		c.logger.Debug("mcp: negotiation step",
			"method", step.method, "offered", step.version,
			"verdict", v.step.String(), "version", v.version)

		switch v.step {
		case verdictModern:
			conn.adoptModern(v)
			c.rememberEra(eraModern)
			return conn, nil

		case verdictLegacy:
			conn.adoptLegacy(v)
			n := &Notification{JSONRPC: jsonrpcVersion, Method: methodInitialized}
			if err := conn.tr.Notify(ctx, n, conn.frame(false)); err != nil {
				return conn, fmt.Errorf("mcp: send %s: %w", methodInitialized, err)
			}
			c.rememberEra(eraLegacy)
			return conn, nil

		case verdictDiscover:
			if mode == protocolLegacy {
				return conn, fmt.Errorf("%w: the server rejected initialize and asked for protocol %s; set protocol to auto or modern",
					errIncompatible, v.version)
			}
			if reprobed {
				return conn, fmt.Errorf("%w: the server kept rejecting the offered protocol versions", errIncompatible)
			}
			reprobed = true
			step = negotiateStep{method: methodDiscover, version: v.version}

		case verdictInitialize:
			if mode == protocolModern {
				return conn, fmt.Errorf("%w: the server does not speak protocol %s; set protocol to auto or legacy",
					errIncompatible, modernProtocolVersion)
			}
			if step.method == methodInitialize {
				if reoffered {
					return conn, fmt.Errorf("%w: the server rejected every offered handshake version", errIncompatible)
				}
				reoffered = true
			}
			step = negotiateStep{method: methodInitialize, version: v.version}

		case verdictRespawn:
			if mode == protocolModern || respawned {
				return conn, fmt.Errorf("mcp: %s: %w", methodDiscover, cause)
			}
			respawned = true
			c.logger.Debug("mcp: server exited during the era probe; restarting it for the legacy handshake",
				"error", cause)
			c.closeConn(conn)
			if conn, err = c.dial(ctx); err != nil {
				return nil, err
			}
			step = negotiateStep{method: methodInitialize, version: v.version}

		default: // verdictFail
			ferr := v.err
			if ferr == nil {
				ferr = fmt.Errorf("%w: negotiation failed", errProtocol)
			}
			if v.thaneBug {
				c.logThaneBug(step.method, ferr)
			}
			return conn, fmt.Errorf("mcp: %s: %w", step.method, ferr)
		}
	}
	return conn, fmt.Errorf("%w: negotiation did not settle", errProtocol)
}

// logThaneBug reports that the server rejected Thane's own framing: at
// ERROR once per run of failed attempts (the supervisor re-arms it after
// a successful connect), at Debug for the retries in between.
func (c *serverClient) logThaneBug(method string, err error) {
	const msg = "mcp: the server rejected Thane's own request framing; this needs a Thane fix"
	if c.thaneBugLogged.CompareAndSwap(false, true) {
		c.logger.Error(msg, "method", method, "error", err)
		return
	}
	c.logger.Debug(msg, "method", method, "error", err)
}

// dial builds one transport whose inbound hooks are bound to a new
// clientConn.
func (c *serverClient) dial(ctx context.Context) (*clientConn, error) {
	conn := &clientConn{}
	tr, err := c.factory(ctx, c.hooksFor(conn))
	if err != nil {
		return nil, fmt.Errorf("mcp: connect: %w", err)
	}
	if tr == nil {
		return nil, fmt.Errorf("%w: the transport factory returned no transport", errUnreachable)
	}
	conn.tr = tr
	return conn, nil
}

// sendDiscover sends server/discover offering version. In auto mode it
// is the era probe and is bounded by min(probeTimeout, attempt
// deadline); in modern mode there is nothing to fall back to, so only
// the attempt deadline applies. On stdio, where a legacy server may
// never answer an unknown request and only the probe timing out reveals
// it, the probe takes at most half of what is left of the attempt, so
// initialize and tools/list still fit. Never cancellable, like
// initialize (basic/utilities/cancellation: clients MUST NOT cancel it).
func (c *serverClient) sendDiscover(ctx context.Context, conn *clientConn, version string) (*Response, error) {
	if c.cfg.protocol != protocolModern {
		bound := c.cfg.probeTimeout
		if dl, ok := ctx.Deadline(); ok && c.cfg.transport == transportStdio {
			bound = min(bound, time.Until(dl)/2)
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, bound)
		defer cancel()
	}
	params, err := marshalParams(map[string]any{"_meta": metaFor(version)})
	if err != nil {
		return nil, err
	}
	req := &Request{JSONRPC: jsonrpcVersion, Method: methodDiscover, Params: params}
	return conn.tr.Call(ctx, req, callInfo{protocolVersion: version, modern: true})
}

// sendInitialize sends the legacy initialize offering version. It
// carries no io.modelcontextprotocol/* _meta key, because a dual-era
// server serves any request with modern _meta statelessly, and no
// MCP-Protocol-Version header, which 2025-11-25 defines only for
// requests after initialize. Never cancellable (2025-11-25
// cancellation.mdx: "The initialize request MUST NOT be cancelled").
func (c *serverClient) sendInitialize(ctx context.Context, conn *clientConn, version string) (*Response, error) {
	params, err := marshalParams(map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{},
		"clientInfo":      thaneClientInfo(),
	})
	if err != nil {
		return nil, err
	}
	req := &Request{JSONRPC: jsonrpcVersion, Method: methodInitialize, Params: params}
	return conn.tr.Call(ctx, req, callInfo{})
}

// adoptModern records a modern verdict on conn.
func (cc *clientConn) adoptModern(v eraVerdict) {
	cc.version = v.version
	if d := v.discover; d != nil {
		cc.server, _ = d.serverInfo()
		cc.instructions = d.Instructions
	}
	cc.eraState.Store(int32(eraModern))
}

// adoptLegacy records a legacy verdict on conn.
func (cc *clientConn) adoptLegacy(v eraVerdict) {
	cc.version = v.version
	if r := v.initialize; r != nil {
		cc.server = r.ServerInfo
		cc.instructions = r.Instructions
	}
	cc.eraState.Store(int32(eraLegacy))
}

// frame returns the callInfo for a request on a negotiated connection:
// modern carries the modern version and modern framing; legacy carries
// the negotiated version only from headerVersionFloor on, the first
// revision that defines the MCP-Protocol-Version header.
func (cc *clientConn) frame(cancellable bool) callInfo {
	if cc.currentEra() == eraModern {
		return callInfo{protocolVersion: cc.version, modern: true, cancellable: cancellable}
	}
	info := callInfo{cancellable: cancellable}
	if versionAtLeast(cc.version, headerVersionFloor) {
		info.protocolVersion = cc.version
	}
	return info
}

// call sends method on a negotiated connection. Modern params always
// carry the per-request _meta; legacy params never carry any
// io.modelcontextprotocol/* key and are omitted when fields is nil. A
// nil error always comes with a response.
func (cc *clientConn) call(ctx context.Context, method string, fields map[string]any, info callInfo) (*Response, error) {
	if cc.currentEra() == eraModern {
		withMeta := make(map[string]any, len(fields)+1)
		for k, v := range fields {
			withMeta[k] = v
		}
		withMeta["_meta"] = metaFor(cc.version)
		fields = withMeta
	}
	var params []byte
	if fields != nil {
		p, err := marshalParams(fields)
		if err != nil {
			return nil, err
		}
		params = p
	}
	resp, err := cc.tr.Call(ctx, &Request{JSONRPC: jsonrpcVersion, Method: method, Params: params}, info)
	if err == nil && resp == nil {
		return nil, fmt.Errorf("%w: no response to %s", errProtocol, method)
	}
	return resp, err
}

// metaFor returns the modern per-request _meta for version.
func metaFor(version string) map[string]any {
	m := modernMeta()
	m[metaProtocolVersion] = version
	return m
}

// serverAnswered reports whether a negotiation reply came from the
// server itself: a JSON-RPC response, or the body of an HTTP 400/404/405
// (era evidence), as opposed to an outage.
func serverAnswered(resp *Response, err error) bool {
	if err == nil {
		return resp != nil
	}
	var statusErr *httpStatusError
	return errors.As(err, &statusErr) && classifyStatus(statusErr.Status) == statusEraEvidence
}

// cachedEra returns the era of the last successful negotiation.
func (c *serverClient) cachedEra() era {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastEra
}

// rememberEra records the era the next attempt starts from.
func (c *serverClient) rememberEra(e era) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastEra = e
}
